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
	watches  map[string]*cameraWatch // by camera IP
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
	// the white arm against the hedge when it's up; each change is journaled.
	Kind string `json:"kind,omitempty"`
}

const (
	motionInterval     = time.Second
	snapshotTimeout    = 5 * time.Second
	snapshotMaxBytes   = 4 << 20
	motionScale        = 8    // snapshots are compared at 1/8 size each way (240 x 135)
	motionPixelDiff    = 24   // a compared pixel changed if its brightness moved this much (of 255) beyond the region's average change
	motionShare        = 0.03 // a region moved if this share of its pixels changed
	brightLevel        = 170  // a pixel is bright at this brightness (of 255)
	brightOnShare      = 0.15 // a "bright" region is on when this share of its pixels are bright (the arm: about 0.2 up, under 0.1 down)
	brightSettleFrames = 2    // a "bright" region changes state after this many frames agree
	motionQuietFrames  = 3    // a region's motion ends after this many still frames
	motionPreroll      = 3    // frames kept from before the first motion
	motionPostroll     = 2    // frames kept after the last region goes still
	motionMaxFrames    = 120  // frames kept per pass, so a region that never settles can't fill the disk
	frameQuality       = 75   // saved frames are half size (960 x 540), about 100 KB
	frameMinFreeBytes  = 2 << 30
	motionFrameDirMode = 0750
)

func NewMotion(dir string, keepDays int, journal *Journal, debugf func(string, ...interface{})) *Motion {
	return &Motion{dir: dir, keepDays: keepDays, journal: journal, debugf: debugf, watches: map[string]*cameraWatch{}}
}

// SetGates gives the lanes, so each pass is journaled with its camera's lane.
func (m *Motion) SetGates(g *Gates) { m.gates.Store(g) }

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

// cameraWatch fetches one camera's snapshot every second and handles each frame.
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
		data, err := fetchSnapshot(client, url)
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
	}
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

// save writes one frame of the current pass, half size.
func (w *cameraWatch) save(f motionFrame) {
	p := w.pass
	if len(p.frames) >= motionMaxFrames {
		p.capped = true
		return
	}
	if len(p.frames) == 0 { // check the disk once per pass
		w.lowDisk = freeBytes(w.m.dir) < frameMinFreeBytes
		if w.lowDisk {
			log.Printf("[MOTION] %s: less than %d GB free on %s; not saving frames", w.ip, frameMinFreeBytes>>30, w.m.dir)
		}
	}
	if w.lowDisk {
		return
	}
	rel := filepath.Join(f.t.Format("2006-01-02"), w.ip, f.t.Format("150405.000")+".jpg")
	small, err := halfJPEG(f.jpeg, frameQuality)
	if err != nil {
		log.Printf("[MOTION] %s: frame not saved: %v", w.ip, err)
		return
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
		if r.Kind != "bright" {
			continue
		}
		share := brightShare(img, r.MotionRegion)
		on := share >= brightOnShare
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

// brightShare is the share of a region's pixels at least brightLevel, at full size.
func brightShare(img image.Image, r MotionRegion) float64 {
	b := img.Bounds()
	x0, x1 := b.Min.X+int(r.Left/100*float64(b.Dx())), b.Min.X+int(r.Right/100*float64(b.Dx()))
	y0, y1 := b.Min.Y+int(r.Top/100*float64(b.Dy())), b.Min.Y+int(r.Bottom/100*float64(b.Dy()))
	ycc, isYCC := img.(*image.YCbCr)
	n, bright := 0, 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			var v uint8
			if isYCC {
				v = ycc.Y[ycc.YOffset(x, y)]
			} else {
				v = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
			}
			n++
			if v >= brightLevel {
				bright++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(bright) / float64(n)
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
