package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"log"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CarCounter counts the cars that come in through a lane, from its overview camera's frames
// (the ones OverviewPhotos keeps, twice a second), with two zones of the picture set on the
// settings page (Car Count Zones, config key "count_zones"): "approach", the lane just before
// the arm, and "beyond", past the arm. A car is counted when "beyond" fills after "approach"
// did. A car seen only in "approach" backed out (at the keypad, or at the arm): it didn't
// come in. Each zone is compared with a background, the median of the last few seconds'
// frames, so a parked or waiting car becomes background only after a while.
//
// Everything is journaled ("car", "car_backed", "beyond_only") and nothing is sent to the
// vehicle log yet: first a week of counts against the plate reads and the arm.
type CarCounter struct {
	mu      sync.Mutex
	cameras map[string]*zoneCounter // by camera IP
	journal *Journal
	debugf  func(string, ...interface{})
	gates   atomic.Pointer[Gates]
}

// CountZone is one zone of one camera's picture: a polygon in percent of its width and height.
type CountZone struct {
	Camera string       `json:"camera"`
	Zone   string       `json:"zone"` // "approach" or "beyond"
	Points [][2]float64 `json:"points"`
	On     float64      `json:"on,omitempty"`     // the zone is full at this share; 0: countOnShare (empty at half)
	Window float64      `json:"window,omitempty"` // approach only: seconds a car may take to reach "beyond"; 0: countApproachWindow
}

const (
	countWidth, countHeight = 160, 90 // frames are compared at this size (1/4 of the Reolink's 640 x 360)
	countPixelDiff          = 30      // a pixel differs from the background by this much (of 255), beyond the picture's typical difference
	countOnShare            = 0.30    // a zone is full at this share of differing pixels (the arm rising through "approach" makes 0.17), and empty below half of it
	countBackground         = 20      // frames in the background's median (every other frame: the last 20 s)
	countWarmup             = 10      // frames before counting starts
	countApproachWindow     = 8 * time.Second

	autoWidth, autoHeight = 96, 54 // the overview photo's "auto" pick compares frames at this size
)

func NewCarCounter(journal *Journal, debugf func(string, ...interface{})) *CarCounter {
	return &CarCounter{cameras: map[string]*zoneCounter{}, journal: journal, debugf: debugf}
}

// SetGates gives the lanes, so each count is journaled with its camera's lane.
func (c *CarCounter) SetGates(g *Gates) { c.gates.Store(g) }

// SetZones sets the zones, keeping the state of a camera whose zones didn't change. A camera
// counts only with both zones. Applied without a restart.
func (c *CarCounter) SetZones(zones []CountZone) {
	byCamera := map[string]map[string]CountZone{}
	for _, z := range zones {
		if byCamera[z.Camera] == nil {
			byCamera[z.Camera] = map[string]CountZone{}
		}
		byCamera[z.Camera][z.Zone] = z
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for ip, zc := range c.cameras {
		if next := byCamera[ip]; !sameZones(next, zc.approachZone, zc.beyondZone) {
			delete(c.cameras, ip)
			log.Printf("[COUNT] Stopped counting cars on %s", ip)
		}
	}
	for ip, z := range byCamera {
		if _, ok := c.cameras[ip]; ok {
			continue
		}
		ap, okA := z["approach"]
		be, okB := z["beyond"]
		if !okA || !okB {
			log.Printf("[CONFIG] WARNING: %s needs both an approach and a beyond zone to count cars", ip)
			continue
		}
		c.cameras[ip] = &zoneCounter{
			ip: ip, approachZone: ap, beyondZone: be,
			approach: polygonMask(ap.Points), beyond: polygonMask(be.Points),
			aOn: shareOr(ap.On), bOn: shareOr(be.On), window: countApproachWindow,
		}
		if ap.Window > 0 {
			c.cameras[ip].window = time.Duration(ap.Window * float64(time.Second))
		}
		log.Printf("[COUNT] Counting cars on %s", ip)
	}
}

func sameZones(z map[string]CountZone, approach, beyond CountZone) bool {
	eq := func(a, b CountZone) bool {
		if len(a.Points) != len(b.Points) || a.On != b.On || a.Window != b.Window {
			return false
		}
		for i := range a.Points {
			if a.Points[i] != b.Points[i] {
				return false
			}
		}
		return true
	}
	return z != nil && eq(z["approach"], approach) && eq(z["beyond"], beyond)
}

// shareOr is a zone's "full" share: its own, or countOnShare.
func shareOr(on float64) float64 {
	if on > 0 {
		return on
	}
	return countOnShare
}

// HandleFrame takes one frame from a camera; frames from a camera without zones are ignored.
func (c *CarCounter) HandleFrame(ip string, f photoFrame) {
	c.mu.Lock()
	zc := c.cameras[ip]
	c.mu.Unlock()
	if zc == nil {
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(f.data))
	if err != nil {
		c.debugf("[COUNT] %s: bad frame: %v", ip, err)
		return
	}
	for _, e := range zc.step(f.at, countGray(img)) {
		if g := c.gates.Load(); g != nil {
			if d, ok := g.ForIP(ip); ok {
				e.data["gate"], e.data["direction"] = d.Gate, d.Direction
			}
		}
		e.data["camera_ip"] = ip
		c.journal.Write(e.kind, e.data)
		log.Printf("[COUNT] %s: %s %v", ip, e.kind, e.data["beyond_start"])
	}
}

// zoneCounter is one camera's zones and state. step is called from one goroutine (the
// camera's photoWatch), so it needs no lock.
type zoneCounter struct {
	ip                   string
	approachZone         CountZone
	beyondZone           CountZone
	approach, beyond     []bool        // masks, countWidth x countHeight
	aOn, bOn             float64       // each zone is full at this share, and empty below half of it
	window               time.Duration // how long after "approach" a car may reach "beyond"
	backedPending        bool          // "approach" emptied without "beyond": backed out, unless "beyond" fills within the window
	history              [][]uint8
	frames               int
	approachOn, beyondOn bool
	approachStart        time.Time
	approachLast         time.Time // the last frame "approach" was full
	reached              bool      // "beyond" filled during this approach
	beyondStart          time.Time
	beyondFromApproach   bool
	beyondPeak, apprPeak float64
}

type countEvent struct {
	kind string
	data map[string]interface{}
}

func (z *zoneCounter) step(t time.Time, g []uint8) []countEvent {
	z.frames++
	if z.frames%2 == 1 { // the background holds every other frame
		z.history = append(z.history, g)
		if len(z.history) > countBackground {
			z.history = z.history[1:]
		}
	}
	if z.frames < countWarmup {
		return nil
	}
	bg := background(z.history)
	if exposureJump(g, bg) {
		return nil // the camera changed its exposure: the whole picture differs, not a car
	}
	a, b := zoneShares(g, bg, z.approach, z.beyond)
	var events []countEvent
	ts := func(t time.Time) string { return t.Local().Format(journalTimeFormat) }

	if a >= z.aOn && !z.approachOn {
		if z.backedPending {
			// Back in "approach" within the window: the same car (a share hovering at the line)
			z.approachOn, z.backedPending = true, false
		} else {
			z.approachOn, z.approachStart, z.reached, z.apprPeak = true, t, false, 0
		}
	}
	if z.approachOn {
		z.approachLast = t
		z.apprPeak = math.Max(z.apprPeak, a)
	}
	if b >= z.bOn && !z.beyondOn {
		z.beyondOn, z.beyondStart, z.beyondPeak = true, t, 0
		// After an approach: a car came in. Without one: something else (a pedestrian, a
		// shadow, or a car backing out through the gate)
		z.beyondFromApproach = z.approachOn || (!z.approachLast.IsZero() && t.Sub(z.approachLast) <= z.window)
		if z.beyondFromApproach {
			z.reached, z.backedPending = true, false
		}
	}
	if z.beyondOn {
		z.beyondPeak = math.Max(z.beyondPeak, b)
	}
	if b < z.bOn/2 && z.beyondOn {
		z.beyondOn = false
		kind := "beyond_only"
		data := map[string]interface{}{
			"beyond_start": ts(z.beyondStart),
			"beyond_end":   ts(t),
			"beyond_peak":  math.Round(z.beyondPeak*100) / 100,
		}
		if z.beyondFromApproach {
			kind = "car"
			data["approach_start"] = ts(z.approachStart)
		}
		events = append(events, countEvent{kind, data})
	}
	if a < z.aOn/2 && z.approachOn {
		z.approachOn = false
		if !z.reached && !z.beyondOn {
			z.backedPending = true // decided when the window has passed
		}
	}
	if z.backedPending && t.Sub(z.approachLast) > z.window {
		z.backedPending = false
		events = append(events, countEvent{"car_backed", map[string]interface{}{
			"approach_start": ts(z.approachStart),
			"approach_end":   ts(z.approachLast),
			"approach_peak":  math.Round(z.apprPeak*100) / 100,
		}})
	}
	return events
}

// countGray reduces a frame to countWidth x countHeight brightness.
func countGray(img image.Image) []uint8 { return reduceTo(img, countWidth, countHeight) }

// reduceTo reduces a frame to w x h brightness, averaging each block.
func reduceTo(img image.Image, w, h int) []uint8 {
	b := img.Bounds()
	out := make([]uint8, w*h)
	ycc, isYCC := img.(*image.YCbCr)
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			sum, n := 0, 0
			for py := y0; py < y1; py++ {
				for px := x0; px < x1; px++ {
					if isYCC {
						sum += int(ycc.Y[ycc.YOffset(px, py)])
					} else {
						r, g, bl, _ := img.At(px, py).RGBA()
						sum += int((299*r + 587*g + 114*bl) / 1000 >> 8)
					}
					n++
				}
			}
			if n > 0 {
				out[y*w+x] = uint8(sum / n)
			}
		}
	}
	return out
}

// background is each pixel's median over the kept frames.
func background(history [][]uint8) []uint8 { return medianFrame(history) }

// medianFrame is each pixel's median over frames of one size.
func medianFrame(history [][]uint8) []uint8 {
	bg := make([]uint8, len(history[0]))
	vals := make([]int, len(history))
	for i := range bg {
		for k, h := range history {
			vals[k] = int(h[i])
		}
		sort.Ints(vals)
		bg[i] = uint8(vals[len(vals)/2])
	}
	return bg
}

// differingShare is the share of a frame's pixels that differ from the background, beyond the
// picture's median difference.
func differingShare(g, bg []uint8) float64 {
	all := make([]bool, len(g))
	for i := range all {
		all[i] = true
	}
	s, _ := zoneShares(g, bg, all, all)
	return s
}

// countExposureJump is how far (of 255) the whole picture's median may move from the
// background before a frame is skipped: .11, in glare, now and then gives one frame far darker.
const countExposureJump = 25

// exposureJump reports whether a frame's median brightness differs from the background's by
// more than countExposureJump.
func exposureJump(g, bg []uint8) bool {
	d := make([]int, len(g))
	for i := range g {
		d[i] = int(g[i]) - int(bg[i])
	}
	sort.Ints(d)
	m := d[len(d)/2]
	return m > countExposureJump || m < -countExposureJump
}

// zoneShares is the share of each zone's pixels that differ from the background, beyond the
// whole picture's median difference, so a cloud or an exposure change doesn't fill a zone.
func zoneShares(g, bg []uint8, approach, beyond []bool) (float64, float64) {
	diff := make([]int, len(g))
	sorted := make([]int, len(g))
	for i := range g {
		diff[i] = int(g[i]) - int(bg[i])
		sorted[i] = diff[i]
	}
	sort.Ints(sorted)
	typical := sorted[len(sorted)/2]
	share := func(mask []bool) float64 {
		n, on := 0, 0
		for i, in := range mask {
			if !in {
				continue
			}
			n++
			d := diff[i] - typical
			if d > countPixelDiff || d < -countPixelDiff {
				on++
			}
		}
		if n == 0 {
			return 0
		}
		return float64(on) / float64(n)
	}
	return share(approach), share(beyond)
}

// polygonMask marks the pixels whose centers are inside a polygon given in percent.
func polygonMask(pts [][2]float64) []bool {
	m := make([]bool, countWidth*countHeight)
	for y := 0; y < countHeight; y++ {
		py := (float64(y) + 0.5) * 100 / countHeight
		for x := 0; x < countWidth; x++ {
			px := (float64(x) + 0.5) * 100 / countWidth
			inside := false
			for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
				xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
				if (yi > py) != (yj > py) && px < (xj-xi)*(py-yi)/(yj-yi)+xi {
					inside = !inside
				}
			}
			m[y*countWidth+x] = inside
		}
	}
	return m
}

func decodeJPEG(data []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(data)) }
