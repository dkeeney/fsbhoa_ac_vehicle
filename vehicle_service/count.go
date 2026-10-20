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
}

const (
	countWidth, countHeight = 160, 90 // frames are compared at this size (1/4 of the Reolink's 640 x 360)
	countPixelDiff          = 30      // a pixel differs from the background by this much (of 255), beyond the picture's typical difference
	countOnShare            = 0.30    // a zone is full at this share of differing pixels (the arm rising through "approach" makes 0.17)
	countOffShare           = 0.15    // and empty again below this
	countBackground         = 20      // frames in the background's median (every other frame: the last 20 s)
	countWarmup             = 10      // frames before counting starts
	countApproachWindow     = 8 * time.Second
)

func NewCarCounter(journal *Journal, debugf func(string, ...interface{})) *CarCounter {
	return &CarCounter{cameras: map[string]*zoneCounter{}, journal: journal, debugf: debugf}
}

// SetGates gives the lanes, so each count is journaled with its camera's lane.
func (c *CarCounter) SetGates(g *Gates) { c.gates.Store(g) }

// SetZones sets the zones, keeping the state of a camera whose zones didn't change. A camera
// counts only with both zones. Applied without a restart.
func (c *CarCounter) SetZones(zones []CountZone) {
	byCamera := map[string]map[string][][2]float64{}
	for _, z := range zones {
		if byCamera[z.Camera] == nil {
			byCamera[z.Camera] = map[string][][2]float64{}
		}
		byCamera[z.Camera][z.Zone] = z.Points
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for ip, zc := range c.cameras {
		if next := byCamera[ip]; !sameZones(next, zc.approachPts, zc.beyondPts) {
			delete(c.cameras, ip)
			log.Printf("[COUNT] Stopped counting cars on %s", ip)
		}
	}
	for ip, z := range byCamera {
		if _, ok := c.cameras[ip]; ok {
			continue
		}
		if z["approach"] == nil || z["beyond"] == nil {
			log.Printf("[CONFIG] WARNING: %s needs both an approach and a beyond zone to count cars", ip)
			continue
		}
		c.cameras[ip] = &zoneCounter{
			ip: ip, approachPts: z["approach"], beyondPts: z["beyond"],
			approach: polygonMask(z["approach"]), beyond: polygonMask(z["beyond"]),
		}
		log.Printf("[COUNT] Counting cars on %s", ip)
	}
}

func sameZones(z map[string][][2]float64, approach, beyond [][2]float64) bool {
	eq := func(a, b [][2]float64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	return z != nil && eq(z["approach"], approach) && eq(z["beyond"], beyond)
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
	approachPts          [][2]float64
	beyondPts            [][2]float64
	approach, beyond     []bool // masks, countWidth x countHeight
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
	a, b := zoneShares(g, background(z.history), z.approach, z.beyond)
	var events []countEvent
	ts := func(t time.Time) string { return t.Local().Format(journalTimeFormat) }

	if a >= countOnShare && !z.approachOn {
		z.approachOn, z.approachStart, z.reached, z.apprPeak = true, t, false, 0
	}
	if z.approachOn {
		z.approachLast = t
		z.apprPeak = math.Max(z.apprPeak, a)
	}
	if b >= countOnShare && !z.beyondOn {
		z.beyondOn, z.beyondStart, z.beyondPeak = true, t, 0
		// After an approach: a car came in. Without one: something else (a pedestrian, a
		// shadow, or a car backing out through the gate)
		z.beyondFromApproach = z.approachOn || (!z.approachLast.IsZero() && t.Sub(z.approachLast) <= countApproachWindow)
		if z.beyondFromApproach {
			z.reached = true
		}
	}
	if z.beyondOn {
		z.beyondPeak = math.Max(z.beyondPeak, b)
	}
	if b < countOffShare && z.beyondOn {
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
	if a < countOffShare && z.approachOn {
		z.approachOn = false
		if !z.reached && !z.beyondOn {
			events = append(events, countEvent{"car_backed", map[string]interface{}{
				"approach_start": ts(z.approachStart),
				"approach_end":   ts(t),
				"approach_peak":  math.Round(z.apprPeak*100) / 100,
			}})
		}
	}
	return events
}

// countGray reduces a frame to countWidth x countHeight brightness, averaging each block.
func countGray(img image.Image) []uint8 {
	b := img.Bounds()
	out := make([]uint8, countWidth*countHeight)
	ycc, isYCC := img.(*image.YCbCr)
	for y := 0; y < countHeight; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/countHeight, b.Min.Y+(y+1)*b.Dy()/countHeight
		for x := 0; x < countWidth; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/countWidth, b.Min.X+(x+1)*b.Dx()/countWidth
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
				out[y*countWidth+x] = uint8(sum / n)
			}
		}
	}
	return out
}

// background is each pixel's median over the kept frames.
func background(history [][]uint8) []uint8 {
	bg := make([]uint8, countWidth*countHeight)
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
