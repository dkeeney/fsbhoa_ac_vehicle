package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Motion watches the lanes' overview and keypad cameras for movement inside the regions set
// on the settings page (Motion Regions, config key "motion_regions"), from one snapshot a
// second. The cameras' own motion events are useless (they report motion all the time), so
// the service compares snapshots itself. For each pass (from the first region that moves to
// a few seconds after the last goes still) it keeps the frames, half size, and journals one
// "motion" entry with each region's start, end and peak, and the frame files. The frames
// are what staff look at when an alert says to (CLAUDE.md, "Overview cameras: motion").
// Read-only: it only fetches the camera's snapshot.
type Motion struct {
	mu       sync.Mutex
	dir      string // frames: <dir>/<date>/<camera IP>/<HHMMSS.mmm>.jpg
	keepDays int
	journal  *Journal
	debugf   func(string, ...interface{})
	gates    atomic.Pointer[Gates]
	watches  map[string]*cameraWatch                        // by camera IP
	frames   *OverviewPhotos                                // frames already being fetched (lanes' overview cameras); nil: none
	onState  func(ip, region string, on bool, at time.Time) // each "bright" or "bar" region's settled state; nil: none
}

// MotionRegion is one area of one camera's picture, in percent of its width and height.
type MotionRegion struct {
	Camera string  `json:"camera"` // the camera's IP address
	Name   string  `json:"name"`   // "gate", "arm", "keypad"
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	// "" watches for motion. "bright" is on while a share of its pixels are bright, such as
	// the white arm against the hedge when it's up; each change is journaled. "bar" is the
	// same, but on while a share of it is a bright bar, brighter than the picture just above
	// and below it (the lowered arm across the pavers), so sun on the pavers doesn't count.
	// "red" is on while enough of it is red (the raised arm's red stripes against grass and
	// curb), or, in a frame without color (infrared at night), while it is a "bar". "ridge"
	// and "edges" find the lowered arm as a line from (Left, Bottom) to (Right, Top)
	// (arm_line.go).
	Kind string `json:"kind,omitempty"`
}

const (
	motionInterval     = time.Second
	snapshotTimeout    = 5 * time.Second
	snapshotMaxBytes   = 4 << 20
	motionScale        = 8    // snapshots are compared at 1/8 size each way (240 x 135)
	motionPixelDiff    = 24   // a compared pixel changed if its brightness moved this much (of 255) beyond the region's average change
	motionShare        = 0.03 // a region moved if this share of its pixels changed
	brightScale        = 4    // "bright" regions are measured on 4 x 4 averages: at full size, sunlit specks in the hedge count as bright
	brightLevel        = 170  // an averaged pixel is bright at this brightness (of 255)
	brightOnShare      = 0.05 // a "bright" region is on when this share of its averaged pixels are bright (the arm, live: 0.10 to 0.11 up, 0.006 to 0.019 down)
	brightSettleFrames = 2    // a "bright" region changes state after this many frames agree
	barContrast        = 40   // a "bar" region's averaged pixel is bar when this much brighter (of 255) than the ones above and below it
	barOffset          = 4.4  // and those are this far away, in percent of the picture's height (16 px of 360: past the arm's width)
	// A "red" region's pixel is red when its red is at least redMin and redOverGreen and
	// redOverBlue more than its green and blue: the raised arm's stripes on .141 are a dark red,
	// about (85, 35, 40), at midday (2026-10-22). On .141's full-size frames of 11:09 to 12:30
	// the box on the raised arm (0 50 8 90) was 2 to 3% red with the arm up (down to 0.2% when
	// dim) and never had a red pixel with the arm down (232 frames, all with a car passing). Its
	// brightness alone didn't separate them at midday, with the sun behind the arm.
	redMin, redOverGreen, redOverBlue = 50, 30, 22
	redOnShare                        = 0.002
	// A frame is without color (a camera in infrared at night) when its mean chroma, |Cb-128| +
	// |Cr-128|, is below this: .141 in daylight 5.5 to 9 (2026-10-22), at night 0.
	colorlessChroma    = 3
	barOnShare         = 0.10 // a "bar" region is on at this share (the arm down on .141, 2026-10-20: 0.29 to 0.36 at its pivot, 0.18 to 0.27 at its tip; up, 0 to 0.03)
	motionQuietFrames  = 3    // a region's motion ends after this many still frames
	motionPreroll      = 3    // frames kept from before the first motion
	motionPostroll     = 2    // frames kept after the last region goes still
	motionMaxFrames    = 240  // frames kept per pass (2 minutes at 2 a second), so a region that never settles can't fill the disk
	frameQuality       = 75   // Speco frames are saved half size (960 x 540), about 100 KB
	frameMinFreeBytes  = 2 << 30
	motionFrameDirMode = 0750
)

func NewMotion(dir string, keepDays int, journal *Journal, debugf func(string, ...interface{})) *Motion {
	return &Motion{dir: dir, keepDays: keepDays, journal: journal, debugf: debugf, watches: map[string]*cameraWatch{}}
}

// SetFrameSource lets a camera whose frames OverviewPhotos already fetches (a lane's overview
// camera, such as the Reolink, which needs its login) be watched from those frames, instead
// of being asked for a second snapshot. Call before SetRegions.
func (m *Motion) SetFrameSource(o *OverviewPhotos) { m.frames = o }

// SetGates gives the lanes, so each pass is journaled with its camera's lane.
func (m *Motion) SetGates(g *Gates) { m.gates.Store(g) }

// SetStateHandler sets who else hears each "bright" or "bar" region's settled state (the arm
// alerts). Call before Run.
func (m *Motion) SetStateHandler(h func(ip, region string, on bool, at time.Time)) { m.onState = h }

// SetRegions starts watching each camera that has a region, restarts a camera whose regions
// changed, and stops one that has none left. Applied without a restart.
func (m *Motion) SetRegions(regions []MotionRegion) {
	byCamera := map[string][]MotionRegion{}
	for _, r := range regions {
		byCamera[r.Camera] = append(byCamera[r.Camera], r)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for ip, w := range m.watches {
		if next, ok := byCamera[ip]; !ok || !reflect.DeepEqual(next, w.regions) {
			close(w.stop)
			delete(m.watches, ip)
			log.Printf("[MOTION] Stopped watching %s", ip)
		}
	}
	for ip, rs := range byCamera {
		if _, running := m.watches[ip]; running {
			continue
		}
		w := m.newWatch(ip, rs)
		m.watches[ip] = w
		go w.run()
		names := make([]string, len(rs))
		for i, r := range rs {
			names[i] = r.Name
		}
		log.Printf("[MOTION] Watching %s: %v", ip, names)
	}
}

// Run deletes saved frames older than keepDays, once an hour.
func (m *Motion) Run() {
	for {
		m.purge()
		time.Sleep(time.Hour)
	}
}

func (m *Motion) purge() {
	days, err := filepath.Glob(filepath.Join(m.dir, "????-??-??"))
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -m.keepDays).Format("2006-01-02")
	for _, d := range days {
		if filepath.Base(d) < cutoff {
			if err := os.RemoveAll(d); err == nil {
				log.Printf("[MOTION] Removed frames %s (older than %d days)", filepath.Base(d), m.keepDays)
			}
		}
	}
}

// cameraWatch handles one camera's frames, one a second: its own snapshot, or the newest
// frame from the frame source when that already fetches the camera.
type cameraWatch struct {
	m       *Motion
	ip      string
	regions []MotionRegion
	stop    chan struct{}
	det     *motionDetector
	recent  []motionFrame // the last few frames, kept for the start of a pass
	pass    *motionPass   // nil between passes
	failing bool
	lowDisk bool
	lastAt  time.Time // the last frame taken from the frame source
	savedTo time.Time // the newest frame of this pass saved; zero between passes
}

type motionFrame struct {
	t    time.Time
	jpeg []byte
}

// motionPass is one stretch of motion on one camera.
type motionPass struct {
	start   time.Time
	post    int      // frames still to keep once every region is still
	frames  []string // saved, relative to the frame folder
	capped  bool
	regions []map[string]interface{}
}

func (m *Motion) newWatch(ip string, regions []MotionRegion) *cameraWatch {
	return &cameraWatch{m: m, ip: ip, regions: regions, stop: make(chan struct{}), det: newMotionDetector(regions)}
}

func (w *cameraWatch) run() {
	client := &http.Client{Timeout: snapshotTimeout}
	url := "http://" + w.ip + "/snapshot.JPG"
	ticker := time.NewTicker(motionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		t := time.Now()
		var data []byte
		var err error
		if f, watched := w.m.frames.Latest(w.ip); watched {
			switch {
			case f.data == nil || t.Sub(f.at) > snapshotTimeout:
				err = fmt.Errorf("no frame in the last %v from the overview photos", snapshotTimeout)
			case !f.at.After(w.lastAt):
				continue // no new frame since the last tick
			default:
				t, data, w.lastAt = f.at, f.data, f.at
			}
		} else {
			data, err = fetchSnapshot(client, url)
		}
		if err != nil {
			if !w.failing {
				log.Printf("[MOTION] %s: snapshot failed: %v", w.ip, err)
				w.m.journal.Write("camera_failed", map[string]interface{}{"camera_ip": w.ip, "error": err.Error()})
				w.failing = true
			}
			continue
		}
		if w.failing {
			log.Printf("[MOTION] %s: snapshots working again", w.ip)
			w.m.journal.Write("camera_ok", map[string]interface{}{"camera_ip": w.ip})
			w.failing = false
		}
		w.handleFrame(t, data)
	}
}

func fetchSnapshot(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, snapshotMaxBytes))
}

// handleFrame compares one snapshot with the last, and starts, continues or ends a pass.
func (w *cameraWatch) handleFrame(t time.Time, data []byte) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		w.m.debugf("[MOTION] %s: bad snapshot: %v", w.ip, err)
		return
	}
	started, ended := w.det.step(t, reduceGray(img, motionScale))
	for _, c := range w.det.brightStates(t, img) {
		entry := map[string]interface{}{"camera_ip": w.ip, "region": c.name, "on": c.on, "at": t.Local().Format(journalTimeFormat), "share": math.Round(c.share*1000) / 1000}
		if g := w.m.gates.Load(); g != nil {
			if d, ok := g.ForIP(w.ip); ok {
				entry["gate"], entry["direction"], entry["device"] = d.Gate, d.Direction, d.Device
			}
		}
		w.m.journal.Write("region_state", entry)
		w.m.debugf("[MOTION] %s: %s %v", w.ip, c.name, c.on)
		if w.m.onState != nil {
			w.m.onState(w.ip, c.name, c.on, t)
		}
	}
	w.armLines(t, img)
	frame := motionFrame{t: t, jpeg: data}

	if w.pass == nil && len(started) > 0 {
		w.pass = &motionPass{start: t}
		w.m.debugf("[MOTION] %s: motion in %v", w.ip, started)
		for _, f := range w.recent {
			w.save(f)
		}
		w.recent = nil
	}
	if w.pass == nil {
		w.recent = append(w.recent, frame)
		if len(w.recent) > motionPreroll {
			w.recent = w.recent[1:]
		}
		return
	}
	w.save(frame)
	w.pass.regions = append(w.pass.regions, ended...)
	if w.det.moving() {
		w.pass.post = motionPostroll + 1 // counting the frame on which the last region settles
		return
	}
	if w.pass.post--; w.pass.post <= 0 {
		w.finish(t)
	}
}

// save writes one frame of the current pass. A camera whose frames come from the overview
// photos (twice a second) also gets the kept frames since the last one saved, so its passes
// have every frame the car count saw, for replaying it (2026-10-22: trucks with trailers).
func (w *cameraWatch) save(f motionFrame) {
	frames := []motionFrame{f}
	if !w.savedTo.IsZero() {
		if between := w.m.frames.Between(w.ip, w.savedTo, f.t); len(between) > 0 {
			frames = frames[:0]
			for _, k := range between {
				frames = append(frames, motionFrame{t: k.at, jpeg: k.data})
			}
			if !frames[len(frames)-1].t.Equal(f.t) {
				frames = append(frames, f)
			}
		}
	}
	w.savedTo = f.t
	for _, f := range frames {
		w.saveOne(f)
	}
}

// saveOne writes one frame: half size when it is wider than overviewPhotoMaxWidth (the Speco
// cameras' 1920 x 1080), as it came otherwise (the Reolink's 640 x 360).
func (w *cameraWatch) saveOne(f motionFrame) {
	p := w.pass
	if len(p.frames) >= motionMaxFrames {
		p.capped = true
		return
	}
	if len(p.frames) == 0 { // check the disk once per pass (every frame while it is low, since none is kept); log only changes
		low := freeBytes(w.m.dir) < frameMinFreeBytes
		if low != w.lowDisk {
			if low {
				log.Printf("[MOTION] %s: less than %d GB free on %s; not saving frames", w.ip, frameMinFreeBytes>>30, w.m.dir)
			} else {
				log.Printf("[MOTION] %s: %d GB free again on %s; saving frames", w.ip, frameMinFreeBytes>>30, w.m.dir)
			}
		}
		w.lowDisk = low
	}
	if w.lowDisk {
		return
	}
	rel := filepath.Join(f.t.Format("2006-01-02"), w.ip, f.t.Format("150405.000")+".jpg")
	small := f.jpeg
	if c, err := jpeg.DecodeConfig(bytes.NewReader(f.jpeg)); err != nil || c.Width > overviewPhotoMaxWidth {
		if small, err = halfJPEG(f.jpeg, frameQuality); err != nil {
			log.Printf("[MOTION] %s: frame not saved: %v", w.ip, err)
			return
		}
	}
	path := filepath.Join(w.m.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), motionFrameDirMode); err != nil {
		log.Printf("[MOTION] %s: %v", w.ip, err)
		return
	}
	if err := os.WriteFile(path, small, 0640); err != nil {
		log.Printf("[MOTION] %s: %v", w.ip, err)
		return
	}
	p.frames = append(p.frames, rel)
}

// finish journals the pass.
func (w *cameraWatch) finish(t time.Time) {
	p := w.pass
	w.pass = nil
	w.savedTo = time.Time{}
	entry := map[string]interface{}{
		"camera_ip": w.ip,
		"start":     p.start.Local().Format(journalTimeFormat),
		"end":       t.Local().Format(journalTimeFormat),
		"regions":   p.regions,
		"frames":    p.frames,
	}
	if p.capped {
		entry["frames_capped"] = true
	}
	if w.lowDisk {
		entry["frames_skipped"] = "low disk"
	}
	if g := w.m.gates.Load(); g != nil {
		if d, ok := g.ForIP(w.ip); ok {
			entry["gate"], entry["direction"], entry["device"] = d.Gate, d.Direction, d.Device
		}
	}
	w.m.journal.Write("motion", entry)
	w.m.debugf("[MOTION] %s: pass %s to %s, %d frames", w.ip, p.start.Format("15:04:05"), t.Format("15:04:05"), len(p.frames))
}

func freeBytes(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(dir), &st); err != nil {
		if err := syscall.Statfs(dir, &st); err != nil {
			return math.MaxUint64 // unknown: don't stop saving
		}
	}
	return st.Bavail * uint64(st.Bsize)
}

// grayFrame is a snapshot's brightness at reduced size.
type grayFrame struct {
	w, h int
	pix  []uint8
}

// reduceGray averages each scale x scale block of the picture's brightness.
func reduceGray(img image.Image, scale int) *grayFrame {
	b := img.Bounds()
	g := &grayFrame{w: b.Dx() / scale, h: b.Dy() / scale}
	g.pix = make([]uint8, g.w*g.h)
	n := scale * scale
	ycc, isYCC := img.(*image.YCbCr)
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			sum := 0
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					px, py := b.Min.X+x*scale+dx, b.Min.Y+y*scale+dy
					if isYCC {
						sum += int(ycc.Y[ycc.YOffset(px, py)])
					} else {
						sum += int(color.GrayModel.Convert(img.At(px, py)).(color.Gray).Y)
					}
				}
			}
			g.pix[y*g.w+x] = uint8(sum / n)
		}
	}
	return g
}

// motionDetector keeps each region's state from frame to frame.
type motionDetector struct {
	prev    *grayFrame
	regions []*regionState
}

type regionState struct {
	MotionRegion
	moving bool
	start  time.Time
	last   time.Time // the last frame that moved
	quiet  int
	peak   float64
	on     bool // "bright" regions: the settled state
	known  bool // "bright" regions: a state has settled since the start
	agree  int  // "bright" regions: frames in a row that differ from the settled state
	// arm line regions (arm_line.go)
	line        string    // the settled state: "down", "broken", "not_down"; "": none yet
	minOuter    float64   // the lowest outer half in this "down" stretch
	brokenSince time.Time // when "broken" settled
	lineAlerted bool      // "arm_line_alert" journaled for this broken stretch
}

type brightChange struct {
	name  string
	on    bool
	share float64
}

// brightStates checks each "bright" region at full size, and returns the ones whose state
// settled on a new value (the first state after the start counts too).
func (d *motionDetector) brightStates(t time.Time, img image.Image) []brightChange {
	var changes []brightChange
	for _, r := range d.regions {
		var share float64
		var on bool
		switch r.Kind {
		case "bright":
			share = brightShare(img, r.MotionRegion)
			on = share >= brightOnShare
		case "bar":
			share = barShare(img, r.MotionRegion)
			on = share >= barOnShare
		case "red":
			if colorless(img) {
				share = barShare(img, r.MotionRegion)
				on = share >= barOnShare
			} else {
				share = redShare(img, r.MotionRegion)
				on = share >= redOnShare
			}
		default:
			continue
		}
		if r.known && on == r.on {
			r.agree = 0
			continue
		}
		if r.agree++; r.agree >= brightSettleFrames {
			r.on, r.known, r.agree = on, true, 0
			changes = append(changes, brightChange{name: r.Name, on: on, share: share})
		}
	}
	return changes
}

// redShare is the share of a region's pixels that are red (redMin, redOverGreen, redOverBlue).
func redShare(img image.Image, r MotionRegion) float64 {
	b := img.Bounds()
	x0, x1 := b.Min.X+int(r.Left/100*float64(b.Dx())), b.Min.X+int(r.Right/100*float64(b.Dx()))
	y0, y1 := b.Min.Y+int(r.Top/100*float64(b.Dy())), b.Min.Y+int(r.Bottom/100*float64(b.Dy()))
	n, red := 0, 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			R, G, B := int(cr>>8), int(cg>>8), int(cb>>8)
			n++
			if R >= redMin && R-G >= redOverGreen && R-B >= redOverBlue {
				red++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(red) / float64(n)
}

// colorless reports whether a frame has no color, as a camera in infrared gives at night: its
// mean chroma, sampled, is below colorlessChroma, or it is a grayscale JPEG.
func colorless(img image.Image) bool {
	if _, gray := img.(*image.Gray); gray {
		return true
	}
	ycc, ok := img.(*image.YCbCr)
	if !ok {
		return false
	}
	sum, n := 0, 0
	for i := 0; i < len(ycc.Cb) && i < len(ycc.Cr); i += 16 {
		cb, cr := int(ycc.Cb[i])-128, int(ycc.Cr[i])-128
		sum += max(cb, -cb) + max(cr, -cr)
		n++
	}
	return n > 0 && sum < colorlessChroma*n
}

// brightShare is the share of a region's brightScale x brightScale averages that are at
// least brightLevel.
func brightShare(img image.Image, r MotionRegion) float64 {
	return cellShare(img, r, func(avg func(x, y int) int, x, y int) bool { return avg(x, y) >= brightLevel })
}

// barShare is the share of a region's brightScale x brightScale averages that are at least
// barContrast brighter than both the averages barOffset above and below them, or both those
// barOffset left and right of them: a bright bar, such as the arm, lying flat (South Enter)
// or steep (North Enter), and not an evenly lit surface or the edge of a shadow.
func barShare(img image.Image, r MotionRegion) float64 {
	off := int(barOffset / 100 * float64(img.Bounds().Dy()))
	return cellShare(img, r, func(avg func(x, y int) int, x, y int) bool {
		v := avg(x, y)
		return (v-avg(x, y-off) >= barContrast && v-avg(x, y+off) >= barContrast) ||
			(v-avg(x-off, y) >= barContrast && v-avg(x+off, y) >= barContrast)
	})
}

// cellShare is the share of a region's brightScale x brightScale averages that pass a test,
// given a function for the average at any point of the picture (clamped to the picture).
func cellShare(img image.Image, r MotionRegion, pass func(avg func(x, y int) int, x, y int) bool) float64 {
	b := img.Bounds()
	x0, x1 := b.Min.X+int(r.Left/100*float64(b.Dx())), b.Min.X+int(r.Right/100*float64(b.Dx()))
	y0, y1 := b.Min.Y+int(r.Top/100*float64(b.Dy())), b.Min.Y+int(r.Bottom/100*float64(b.Dy()))
	ycc, isYCC := img.(*image.YCbCr)
	avg := func(x, y int) int {
		x = max(b.Min.X, min(b.Max.X-brightScale, x))
		y = max(b.Min.Y, min(b.Max.Y-brightScale, y))
		sum := 0
		for dy := 0; dy < brightScale; dy++ {
			for dx := 0; dx < brightScale; dx++ {
				if isYCC {
					sum += int(ycc.Y[ycc.YOffset(x+dx, y+dy)])
				} else {
					sum += int(color.GrayModel.Convert(img.At(x+dx, y+dy)).(color.Gray).Y)
				}
			}
		}
		return sum / (brightScale * brightScale)
	}
	n, on := 0, 0
	for y := y0; y+brightScale <= y1; y += brightScale {
		for x := x0; x+brightScale <= x1; x += brightScale {
			n++
			if pass(avg, x, y) {
				on++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(on) / float64(n)
}

func newMotionDetector(regions []MotionRegion) *motionDetector {
	d := &motionDetector{}
	for _, r := range regions {
		d.regions = append(d.regions, &regionState{MotionRegion: r})
	}
	return d
}

// step compares a frame with the one before. It returns the regions that started moving,
// and a summary of each region whose motion ended.
func (d *motionDetector) step(t time.Time, g *grayFrame) (started []string, ended []map[string]interface{}) {
	prev := d.prev
	d.prev = g
	if prev == nil || prev.w != g.w || prev.h != g.h {
		return nil, nil
	}
	for _, r := range d.regions {
		if r.Kind != "" {
			continue
		}
		share := regionShare(prev, g, r.MotionRegion)
		if share >= motionShare {
			if !r.moving {
				r.moving, r.start, r.peak = true, t, 0
				started = append(started, r.Name)
			}
			r.last, r.quiet = t, 0
			r.peak = math.Max(r.peak, share)
			continue
		}
		if r.moving {
			if r.quiet++; r.quiet >= motionQuietFrames {
				r.moving = false
				ended = append(ended, map[string]interface{}{
					"region": r.Name,
					"start":  r.start.Local().Format(journalTimeFormat),
					"end":    r.last.Local().Format(journalTimeFormat),
					"peak":   math.Round(r.peak*1000) / 1000,
				})
			}
		}
	}
	return started, ended
}

func (d *motionDetector) moving() bool {
	for _, r := range d.regions {
		if r.moving {
			return true
		}
	}
	return false
}

// regionShare is the share of a region's pixels that changed between two frames, beyond the
// region's average change (so a cloud or the camera's exposure doesn't count as motion).
func regionShare(a, b *grayFrame, r MotionRegion) float64 {
	x0, x1 := int(r.Left/100*float64(b.w)), int(r.Right/100*float64(b.w))
	y0, y1 := int(r.Top/100*float64(b.h)), int(r.Bottom/100*float64(b.h))
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, b.w), min(y1, b.h)
	n := (x1 - x0) * (y1 - y0)
	if n <= 0 {
		return 0
	}
	diffs := make([]int, 0, n)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*b.w + x
			diffs = append(diffs, int(b.pix[i])-int(a.pix[i]))
		}
	}
	// The median change is the region's overall shift; a car changes only part of it.
	sorted := append([]int(nil), diffs...)
	sort.Ints(sorted)
	shift := sorted[len(sorted)/2]
	changed := 0
	for _, d := range diffs {
		if d-shift > motionPixelDiff || shift-d > motionPixelDiff {
			changed++
		}
	}
	return float64(changed) / float64(n)
}

// halfJPEG reduces a JPEG to half size each way. Brightness is averaged; color is sampled.
func halfJPEG(data []byte, quality int) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	ycc, ok := src.(*image.YCbCr)
	if !ok {
		return shrinkJPEG(data, 2, quality)
	}
	dst := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := b.Min.X+2*x, b.Min.Y+2*y
			sum := int(ycc.Y[ycc.YOffset(sx, sy)]) + int(ycc.Y[ycc.YOffset(sx+1, sy)]) +
				int(ycc.Y[ycc.YOffset(sx, sy+1)]) + int(ycc.Y[ycc.YOffset(sx+1, sy+1)])
			dst.Y[dst.YOffset(x, y)] = uint8(sum / 4)
			if x%2 == 0 && y%2 == 0 {
				ci := ycc.COffset(sx, sy)
				di := dst.COffset(x, y)
				dst.Cb[di], dst.Cr[di] = ycc.Cb[ci], ycc.Cr[ci]
			}
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// armLines journals each arm line region's settled state, and a broken arm that has lasted the
// lane's arm alert wait. Watch only: nothing goes to the arm alerts yet.
func (w *cameraWatch) armLines(t time.Time, img image.Image) {
	after := armBrokenAfter
	var lane map[string]interface{}
	if g := w.m.gates.Load(); g != nil {
		if d, ok := g.ArmAlertAfter(w.ip); ok {
			after = d
		}
		if d, ok := g.ForIP(w.ip); ok {
			lane = map[string]interface{}{"gate": d.Gate, "direction": d.Direction, "device": d.Device}
		}
	}
	round := func(v float64) float64 { return math.Round(v*100) / 100 }
	for _, c := range w.det.lineStates(t, img, after) {
		kind := "arm_line"
		entry := map[string]interface{}{"camera_ip": w.ip, "region": c.name, "state": c.state, "at": t.Local().Format(journalTimeFormat),
			"cover": round(c.score.cover), "pivot": round(c.score.pivot), "outer": round(c.score.outer)}
		if c.minOuter >= 0 {
			entry["down_min_outer"] = round(c.minOuter)
		}
		if c.alert == "arm_line_alert" {
			kind = c.alert // the state didn't change; the broken arm lasted the wait
			delete(entry, "state")
		}
		if c.alert != "" {
			entry["broken_since"] = c.since.Local().Format(journalTimeFormat)
		}
		for k, v := range lane {
			entry[k] = v
		}
		w.m.journal.Write(kind, entry)
		if c.alert == "arm_line_ok" {
			w.m.journal.Write("arm_line_ok", map[string]interface{}{"camera_ip": w.ip, "region": c.name, "at": entry["at"], "broken_since": entry["broken_since"]})
		}
		w.m.debugf("[MOTION] %s: %s %s (cover %.2f, pivot %.2f, outer %.2f) %s", w.ip, c.name, c.state, c.score.cover, c.score.pivot, c.score.outer, c.alert)
	}
}
