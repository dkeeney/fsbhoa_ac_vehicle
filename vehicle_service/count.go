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
// frames, so a parked or waiting car becomes background only after a while. The comparison is
// by texture, block by block (blockChanges): headlights on the pavers and the camera darkening
// its picture for a white car change brightness, not the pavers' pattern.
//
// Everything is journaled ("car", "car_backed", "beyond_only", "arm_lowered") and nothing is sent to the
// vehicle log yet: first a week of counts against the plate reads and the arm.
type CarCounter struct {
	mu      sync.Mutex
	cameras map[string]*zoneCounter // by camera IP
	journal *Journal
	debugf  func(string, ...interface{})
	gates   atomic.Pointer[Gates]
	unread  *UnreadCars // cars with no plate read, for cameras with plate=; nil: none
	// Takes a car that backed out of an entrance lane, for cameras with plate=: the camera,
	// the lane, the plate= window and the moment for its photo (UnreadCars.Backed). nil: journal only
	backed func(camera, gate string, e countEvent, after time.Duration, photoAt time.Time)
	// The lane's arm, told of each car through it: returns a note for the car's card and its
	// photo (an arm already up). nil: none
	arm func(gate string, at time.Time) (string, []byte)
	// Whether the lane's arm was up at a moment (ArmAlerts.ArmUp). nil: not known
	armUp func(gate string, at time.Time) bool
	// Whether the lane's arm came down between two moments (ArmAlerts.ArmCameDown). nil: not known
	armCameDown func(gate string, from, to time.Time) bool
}

// CountZone is one zone of one camera's picture: a polygon in percent of its width and height.
type CountZone struct {
	Camera string       `json:"camera"`
	Zone   string       `json:"zone"` // "approach" or "beyond"
	Points [][2]float64 `json:"points"`
	On     float64      `json:"on,omitempty"`     // the zone is full at this share; 0: countOnShare (empty at half)
	Window float64      `json:"window,omitempty"` // approach only: seconds a car may take to reach "beyond"; 0: countApproachWindow
	Jump   float64      `json:"jump,omitempty"`   // approach only: how far the picture may move before a frame is skipped; 0: countExposureJump
	Plate  float64      `json:"plate,omitempty"`  // beyond only: seconds after a car enters "beyond" its plate read may be captured; 0: no check (unread.go)
	Photo  float64      `json:"photo,omitempty"`  // beyond only: the same matching, only for the read's overview photo (no cars reported unread); 0: none
}

const (
	countWidth, countHeight = 320, 176 // frames are compared at this size (half the Reolink's 640 x 360, to whole blocks)
	countBlock              = 8        // in blocks of countBlock x countBlock pixels
	blocksW, blocksH        = countWidth / countBlock, countHeight / countBlock
	countPixelDiff          = 30   // a pixel (or a plain block) differs from the background by this much (of 255), beyond the picture's typical difference
	countTextured           = 5    // a block whose brightness varies by this much (standard deviation) has a pattern to compare
	countSameTexture        = 0.5  // a textured block whose pattern correlates with the background's this well is unchanged, however much brighter or darker
	countOnShare            = 0.30 // a zone is full at this share of changed blocks, and empty below half of it
	countBackground         = 40   // frames in the background's median (every other frame: the last 40 s; 20 s until 2026-10-23, when a car waiting 10 s at the keypad or the arm faded into it)
	countWarmup             = 10   // frames before counting starts
	countApproachWindow     = 8 * time.Second
	// The lane's front (lanePath): a car whose front falls back this far along the lane (the
	// middle of "approach" 0, of "beyond" 1) in steps of at most countPathJump, while it is still
	// in view, reached past the arm and reversed (2026-10-22 13:08, an SUV: its front went 1.02
	// to 0.27 in steps of up to 0.18; cars going in reach 1.4 to 1.6 and leave the picture).
	// A bigger step is another object (the next car, or the arm coming down behind a car that
	// left: 1.38 to 0.85 at 18:46 that night). After "beyond" empties, the reversal is waited for
	// up to countPathWait.
	countWentBack   = 0.45
	countPathJump   = 0.35
	countPathWait   = 5 * time.Second
	countPathMargin = 2

	// A back-out whose time in "approach" (from this much before it to this much after) holds
	// the arm coming down was the arm (classify)
	armLoweredBefore, armLoweredAfter = 1 * time.Second, 4 * time.Second

	autoWidth, autoHeight = 96, 54 // the overview photo's "auto" pick compares frames at this size
)

func NewCarCounter(journal *Journal, debugf func(string, ...interface{})) *CarCounter {
	return &CarCounter{cameras: map[string]*zoneCounter{}, journal: journal, debugf: debugf}
}

// SetArm sets what each counted car is reported to (ArmAlerts.CarCrossed), and how to ask
// whether the lane's arm was up (ArmAlerts.ArmUp) and whether it came down (ArmAlerts.ArmCameDown).
// Call before frames arrive.
func (c *CarCounter) SetArm(arm func(gate string, at time.Time) (string, []byte), armUp func(gate string, at time.Time) bool, armCameDown func(gate string, from, to time.Time) bool) {
	c.arm, c.armUp, c.armCameDown = arm, armUp, armCameDown
}

// SetUnread sets where counted cars go to be matched with plate reads. Call before frames arrive.
func (c *CarCounter) SetUnread(u *UnreadCars) { c.unread = u }

// SetBacked sets where a car that backed out goes (UnreadCars.Backed). Call before frames arrive.
func (c *CarCounter) SetBacked(send func(camera, gate string, e countEvent, after time.Duration, photoAt time.Time)) {
	c.backed = send
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
			aOn: shareOr(ap.On), bOn: shareOr(be.On), window: countApproachWindow, jump: countExposureJump,
		}
		if ap.Window > 0 {
			c.cameras[ip].window = time.Duration(ap.Window * float64(time.Second))
		}
		if ap.Jump > 0 {
			c.cameras[ip].jump = int(ap.Jump)
		}
		log.Printf("[COUNT] Counting cars on %s", ip)
	}
}

func sameZones(z map[string]CountZone, approach, beyond CountZone) bool {
	eq := func(a, b CountZone) bool {
		if len(a.Points) != len(b.Points) || a.On != b.On || a.Window != b.Window || a.Jump != b.Jump || a.Plate != b.Plate || a.Photo != b.Photo {
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
		e = c.classify(ip, e)
		c.journal.Write(e.kind, e.data)
		log.Printf("[COUNT] %s: %s %v", ip, e.kind, e.data["beyond_start"])
		gate, _ := e.data["gate"].(string)
		if gate == "" || e.data["direction"] != "entry" {
			continue
		}
		// A car that backed out (at the keypad, or at the arm) is a clue for staff: a driver
		// who had to back up and enter a code again, or one let in by someone else. Sent only
		// where the count is matched with the plate reads (plate=), where it holds up.
		if e.kind == "car_backed" && c.backed != nil && zc.beyondZone.Plate > 0 {
			photoAt := e.approach.Add(e.end.Sub(e.approach) / 2) // halfway through its time in "approach"
			if e.data["went_back"] == true {
				photoAt = e.start // as it reached past the arm
			}
			c.backed(ip, gate, e, time.Duration(zc.beyondZone.Plate*float64(time.Second)), photoAt)
		}
		if e.kind != "car" {
			continue
		}
		var note string
		var notePhoto []byte
		if c.arm != nil {
			note, notePhoto = c.arm(gate, e.start)
		}
		// Matched with the lane's plate reads: for its photo and note, and with plate= for a
		// car with no read; photo= where the time to the read varies too much for that
		if c.unread != nil && (zc.beyondZone.Plate > 0 || zc.beyondZone.Photo > 0) {
			window, report := zc.beyondZone.Plate, true
			if window == 0 {
				window, report = zc.beyondZone.Photo, false
			}
			c.unread.Car(ip, gate, e.approach, e.start, e.end, time.Duration(window*float64(time.Second)), note, notePhoto, report)
		}
	}
}

// classify tags a count with its camera and lane, and makes "beyond" filling while the lane's
// arm is up a car, even with no "approach" before it: 16 of the 20 beyond_only at South Enter
// from 2026-10-20 to 22 came during an arm cycle, 13 with a plate read 2 to 4 s later (cars
// that went past the keypad without filling "approach" enough, and a long vehicle whose front
// reached "beyond" first).
func (c *CarCounter) classify(ip string, e countEvent) countEvent {
	var lane GateDevice
	if g := c.gates.Load(); g != nil {
		if d, ok := g.ForIP(ip); ok {
			lane = d
			e.data["gate"], e.data["direction"] = d.Gate, d.Direction
		}
	}
	if e.kind == "beyond_only" && e.data["went_back"] == nil && lane.Direction == "entry" && c.armUp != nil && c.armUp(lane.Gate, e.start) {
		e.kind, e.approach = "car", e.start
		e.data["arm_up"] = true
	}
	// "approach" filling and emptying as the arm came down was the arm: at night it glows in
	// the infrared and sweeps through "approach" after each car (2026-10-22 18:30 to 20:15: 16
	// back-outs, each 1.5 to 2.5 s before the arm was seen down)
	if e.kind == "car_backed" && e.data["went_back"] == nil && lane.Direction == "entry" && c.armCameDown != nil &&
		c.armCameDown(lane.Gate, e.approach.Add(-armLoweredBefore), e.end.Add(armLoweredAfter)) {
		e.kind = "arm_lowered"
	}
	e.data["camera_ip"] = ip
	return e
}

// zoneCounter is one camera's zones and state. step is called from one goroutine (the
// camera's photoWatch), so it needs no lock.
type zoneCounter struct {
	ip                   string
	approachZone         CountZone
	beyondZone           CountZone
	approach, beyond     []bool        // masks, blocksW x blocksH
	aOn, bOn             float64       // each zone is full at this share, and empty below half of it
	window               time.Duration // how long after "approach" a car may reach "beyond"
	jump                 int           // a frame whose whole picture moved more than this from the background is skipped
	backedPending        bool          // "approach" emptied without "beyond": backed out, unless "beyond" fills within the window
	history              [][]uint8
	bg                   []uint8 // the background: the median of history, kept until history changes
	frames               int
	approachOn, beyondOn bool
	approachStart        time.Time
	approachLast         time.Time // the last frame "approach" was full
	reached              bool      // "beyond" filled during this approach
	beyondStart          time.Time
	beyondFromApproach   bool
	beyondPeak, apprPeak float64
	path                 lanePath
	pending              *countEvent // "beyond" emptied: a car in, unless it reverses (decided within countPathWait)
	reversedInBeyond     bool        // the front fell back while "beyond" was full
	backMax, backFront   float64     // how far it got and where it fell back to, for the journal
}

// lanePath follows the front of what is in the lane (both zones): the 90th percentile of the
// positions of the biggest group of touching changed blocks, along the line from the middle of
// "approach" (0) to the middle of "beyond" (1), and the furthest it got since it was last
// picked up. Only the biggest group, so something left over elsewhere in the lane (a shadow
// that moved) doesn't hold the front.
type lanePath struct {
	union          []bool // both zones, and countPathMargin blocks around them (they don't touch: a car across the arm is one group)
	a0, a1         [2]float64
	min            int // fewer changed blocks than this: nothing in the lane
	on             bool
	front, maxSeen float64
}

func newLanePath(approach, beyond []bool) lanePath {
	p := lanePath{union: make([]bool, len(approach)), a0: maskCenter(approach), a1: maskCenter(beyond)}
	n := 0
	for i := range approach {
		if !approach[i] && !beyond[i] {
			continue
		}
		n++
		x, y := i%blocksW, i/blocksW
		for dy := -countPathMargin; dy <= countPathMargin; dy++ {
			for dx := -countPathMargin; dx <= countPathMargin; dx++ {
				if x+dx >= 0 && x+dx < blocksW && y+dy >= 0 && y+dy < blocksH {
					p.union[(y+dy)*blocksW+x+dx] = true
				}
			}
		}
	}
	p.min = max(4, n/20)
	return p
}

// update takes a frame's changed blocks, and reports whether the front was picked up anew
// (after nothing in the lane, or another object).
func (p *lanePath) update(changed []bool) bool {
	dx, dy := p.a1[0]-p.a0[0], p.a1[1]-p.a0[1]
	l2 := dx*dx + dy*dy
	var ss []float64
	for _, i := range biggestGroup(changed, p.union) {
		if l2 == 0 {
			break
		}
		x := (float64(i%blocksW) + 0.5) * 160 / blocksW
		y := (float64(i/blocksW) + 0.5) * 90 / blocksH
		ss = append(ss, ((x-p.a0[0])*dx+(y-p.a0[1])*dy)/l2)
	}
	if len(ss) < p.min {
		p.on = false
		return false
	}
	sort.Float64s(ss)
	f := ss[len(ss)*9/10]
	picked := !p.on || math.Abs(f-p.front) > countPathJump
	if picked {
		p.on, p.maxSeen = true, f
	}
	p.front, p.maxSeen = f, math.Max(p.maxSeen, f)
	return picked
}

// biggestGroup is the biggest group of changed blocks inside a mask that touch side by side.
func biggestGroup(changed, mask []bool) []int {
	seen := make([]bool, len(changed))
	var best []int
	for i := range changed {
		if !changed[i] || !mask[i] || seen[i] {
			continue
		}
		group := []int{i}
		seen[i] = true
		for k := 0; k < len(group); k++ {
			j := group[k]
			x, y := j%blocksW, j/blocksW
			for _, n := range [4][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
				if n[0] < 0 || n[0] >= blocksW || n[1] < 0 || n[1] >= blocksH {
					continue
				}
				m := n[1]*blocksW + n[0]
				if changed[m] && mask[m] && !seen[m] {
					seen[m] = true
					group = append(group, m)
				}
			}
		}
		if len(group) > len(best) {
			best = group
		}
	}
	return best
}

// reversed reports whether the front has fallen back countWentBack from the furthest it got.
func (p *lanePath) reversed() bool { return p.on && p.front <= p.maxSeen-countWentBack }

type countEvent struct {
	kind                 string
	data                 map[string]interface{}
	approach, start, end time.Time // a car: "approach" filled, "beyond" filled and emptied; backed out: "approach" filled and emptied (end)
}

func (z *zoneCounter) step(t time.Time, g []uint8) []countEvent {
	z.frames++
	if z.frames%2 == 1 { // the background holds every other frame
		z.history = append(z.history, g)
		if len(z.history) > countBackground {
			z.history = z.history[1:]
		}
		z.bg = nil
	}
	if z.frames < countWarmup {
		return nil
	}
	if z.bg == nil {
		z.bg = background(z.history)
	}
	bg := z.bg
	if exposureJump(g, bg, z.jump) {
		return nil // the camera changed its exposure: the whole picture differs, not a car
	}
	changed := blockChanges(g, bg)
	a, b := zoneShares(changed, z.approach, z.beyond)
	if z.path.union == nil {
		z.path = newLanePath(z.approach, z.beyond)
	}
	picked := z.path.update(changed)
	var events []countEvent
	ts := func(t time.Time) string { return t.Local().Format(journalTimeFormat) }

	// "beyond" emptied: decided when it reverses (went back), or leaves, or is replaced by
	// another object (a car in), or "beyond" fills again, or countPathWait passes
	if z.pending != nil {
		if z.path.reversed() {
			events = append(events, z.wentBack(*z.pending))
			z.pending = nil
		} else if !z.path.on || picked || b >= z.bOn || t.Sub(z.pending.end) > countPathWait {
			events = append(events, *z.pending)
			z.pending = nil
		}
	}

	if a >= z.aOn && !z.approachOn {
		if z.backedPending {
			// Back in "approach" within the window: the same car (a share hovering at the line)
			z.approachOn, z.backedPending = true, false
		} else {
			z.approachOn, z.approachStart, z.reached, z.apprPeak = true, t, false, 0
		}
		// Something already past the arm as "approach" fills: one long vehicle across both zones
		// (2026-10-20 12:31, a truck towing a wood chipper: "beyond" filled 0.5 s before
		// "approach"), or a car close behind one leaving. Either way, not a car backing out.
		if z.beyondOn {
			z.reached = true
		}
	}
	if z.approachOn {
		z.approachLast = t
		z.apprPeak = math.Max(z.apprPeak, a)
	}
	if b >= z.bOn && !z.beyondOn {
		z.beyondOn, z.beyondStart, z.beyondPeak, z.reversedInBeyond = true, t, 0, false
		// After an approach: a car came in. Without one: something else (a pedestrian, a
		// shadow, or a car backing out through the gate)
		z.beyondFromApproach = z.approachOn || (!z.approachLast.IsZero() && t.Sub(z.approachLast) <= z.window)
		if z.beyondFromApproach {
			z.reached, z.backedPending = true, false
		}
	}
	if z.beyondOn {
		z.beyondPeak = math.Max(z.beyondPeak, b)
		if z.path.reversed() && !z.reversedInBeyond {
			z.reversedInBeyond, z.backMax, z.backFront = true, z.path.maxSeen, z.path.front
		}
	}
	if b < z.bOn/2 && z.beyondOn {
		z.beyondOn = false
		kind := "beyond_only"
		data := map[string]interface{}{
			"beyond_start": ts(z.beyondStart),
			"beyond_end":   ts(t),
			"beyond_peak":  math.Round(z.beyondPeak*100) / 100,
		}
		e := countEvent{kind: kind, data: data, start: z.beyondStart, end: t}
		if z.beyondFromApproach {
			e.kind = "car"
			data["approach_start"] = ts(z.approachStart)
			e.approach = z.approachStart
		}
		if z.reversedInBeyond || z.path.reversed() {
			events = append(events, z.wentBack(e)) // already falling back toward the arm
		} else {
			z.pending = &e
		}
	}
	if a < z.aOn/2 && z.approachOn {
		z.approachOn = false
		if !z.reached && !z.beyondOn {
			z.backedPending = true // decided when the window has passed
		}
	}
	if z.backedPending && t.Sub(z.approachLast) > z.window {
		z.backedPending = false
		events = append(events, countEvent{kind: "car_backed", data: map[string]interface{}{
			"approach_start": ts(z.approachStart),
			"approach_end":   ts(z.approachLast),
			"approach_peak":  math.Round(z.apprPeak*100) / 100,
		}, approach: z.approachStart, end: z.approachLast})
	}
	return events
}

// wentBack makes a "beyond" pass that reversed toward the arm a car that backed out (or, with
// no approach before it, a beyond_only that went back).
func (z *zoneCounter) wentBack(e countEvent) countEvent {
	if !z.reversedInBeyond {
		z.backMax, z.backFront = z.path.maxSeen, z.path.front
	}
	e.data["went_back"] = true
	e.data["front_max"] = math.Round(z.backMax*100) / 100
	e.data["front_back"] = math.Round(z.backFront*100) / 100
	if e.kind == "car" {
		e.kind = "car_backed"
	}
	return e
}

// maskCenter is the middle of a block mask, in 1/160 of the picture's width and 1/90 of its height.
func maskCenter(m []bool) [2]float64 {
	var sx, sy, n float64
	for i, in := range m {
		if in {
			sx += (float64(i%blocksW) + 0.5) * 160 / blocksW
			sy += (float64(i/blocksW) + 0.5) * 90 / blocksH
			n++
		}
	}
	if n == 0 {
		return [2]float64{}
	}
	return [2]float64{sx / n, sy / n}
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
	d := make([]int, len(g))
	sorted := make([]int, len(g))
	for i := range g {
		d[i] = int(g[i]) - int(bg[i])
		sorted[i] = d[i]
	}
	sort.Ints(sorted)
	typical := sorted[len(sorted)/2]
	on := 0
	for _, v := range d {
		if v-typical > countPixelDiff || v-typical < -countPixelDiff {
			on++
		}
	}
	return float64(on) / float64(len(d))
}

// countExposureJump is how far (of 255) the whole picture's median may move from the
// background before a frame is skipped: .11, in glare, now and then gives one frame far darker
// (28 to 69 in its recordings of 2026-10-20 and 21). A camera's approach line can set its own
// (jump=): South Enter's Reolink darkens its whole picture by 28 to 35 for a white car close by
// in "beyond", so with 25 those cars' frames were skipped and they were journaled as backed out
// (9 in the Bingo hold of 2026-10-22, 17:00 to 18:13).
const countExposureJump = 25

// exposureJump reports whether a frame's median brightness differs from the background's by
// more than limit.
func exposureJump(g, bg []uint8, limit int) bool {
	d := make([]int, len(g))
	for i := range g {
		d[i] = int(g[i]) - int(bg[i])
	}
	sort.Ints(d)
	m := d[len(d)/2]
	return m > limit || m < -limit
}

// blockChanges marks the blocks (blocksW x blocksH) where something is in the picture that
// isn't in the background. A block with a pattern in both (the pavers) has changed when its
// pattern no longer correlates with the background's: headlights on the pavers, a shadow or
// the camera's exposure make it brighter or darker but keep the pattern (2026-10-22: at night
// the headlights of a car at the keypad filled "approach" by brightness, and by day the
// Reolink's darkening for a white car hid it). Other blocks have changed when their brightness
// moved more than countPixelDiff beyond the picture's typical change. Then lone thin lines
// are dropped (a block stays only within a 2 x 2 square of changed blocks): the arm, which
// glows in the infrared at night, and the edge it leaves where it was.
func blockChanges(g, bg []uint8) []bool {
	const n = countBlock * countBlock
	means := make([]float64, blocksW*blocksH)
	textured := make([]bool, blocksW*blocksH)
	same := make([]bool, blocksW*blocksH)
	for by := 0; by < blocksH; by++ {
		for bx := 0; bx < blocksW; bx++ {
			var sf, sb, sff, sbb, sfb float64
			for y := by * countBlock; y < (by+1)*countBlock; y++ {
				for x := bx * countBlock; x < (bx+1)*countBlock; x++ {
					f, b := float64(g[y*countWidth+x]), float64(bg[y*countWidth+x])
					sf, sb, sff, sbb, sfb = sf+f, sb+b, sff+f*f, sbb+b*b, sfb+f*b
				}
			}
			mf, mb := sf/n, sb/n
			vf, vb := sff/n-mf*mf, sbb/n-mb*mb
			i := by*blocksW + bx
			means[i] = mf - mb
			if vf > countTextured*countTextured && vb > countTextured*countTextured {
				textured[i] = true
				same[i] = (sfb/n-mf*mb)/math.Sqrt(vf*vb) >= countSameTexture
			}
		}
	}
	sorted := append([]float64(nil), means...)
	sort.Float64s(sorted)
	typical := sorted[len(sorted)/2]
	raw := make([]bool, len(means))
	for i := range raw {
		if textured[i] {
			raw[i] = !same[i]
		} else {
			raw[i] = math.Abs(means[i]-typical) > countPixelDiff
		}
	}
	// Opening: keep each block that is in a 2 x 2 square of changed blocks
	out := make([]bool, len(raw))
	for y := 0; y+1 < blocksH; y++ {
		for x := 0; x+1 < blocksW; x++ {
			i := y*blocksW + x
			if raw[i] && raw[i+1] && raw[i+blocksW] && raw[i+blocksW+1] {
				out[i], out[i+1], out[i+blocksW], out[i+blocksW+1] = true, true, true, true
			}
		}
	}
	return out
}

// zoneShares is the share of each zone's blocks that changed (blockChanges).
func zoneShares(changed, approach, beyond []bool) (float64, float64) {
	share := func(mask []bool) float64 {
		n, on := 0, 0
		for i, in := range mask {
			if !in {
				continue
			}
			n++
			if changed[i] {
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

// polygonMask marks the blocks whose centers are inside a polygon given in percent.
func polygonMask(pts [][2]float64) []bool {
	m := make([]bool, blocksW*blocksH)
	for y := 0; y < blocksH; y++ {
		py := (float64(y) + 0.5) * 100 / blocksH
		for x := 0; x < blocksW; x++ {
			px := (float64(x) + 0.5) * 100 / blocksW
			inside := false
			for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
				xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
				if (yi > py) != (yj > py) && px < (xj-xi)*(py-yi)/(yj-yi)+xi {
					inside = !inside
				}
			}
			m[y*blocksW+x] = inside
		}
	}
	return m
}

func decodeJPEG(data []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(data)) }
