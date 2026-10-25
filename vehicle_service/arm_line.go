package main

import (
	"image"
	"image/color"
	"math"
	"time"
)

// Arm line regions find the lowered arm as a line across the picture, instead of the brightness
// of two boxes on it (CLAUDE.md, "The arm line finder"). The "bar" regions fail on North Enter's
// .11 at night, where in infrared the arm's tip is no brighter than the curb behind it, so its
// broken arm alert has been off since 2026-10-22. A line region's four numbers are the lowered
// arm's two ends, in percent of the picture: its pivot end (left, bottom) and its tip end
// (right, top), so the arm rises to the right, as both entrances' arms do. The service tries
// lines near those ends and keeps the one best covered by the arm.
//
//   - "ridge" (.11): a point is covered where the line is brighter or darker than both its
//     sides, a thin stripe. Found at 480 x 270. Recorded 2026-10-24 and 25 (18,214 frames, dawn,
//     overcast day, night): down in all frames with the arm down, the outer half at least 0.47.
//   - "edges" (.141, the Reolink): the arm's two borders, at a few widths either side of the
//     line, both have a strong brightness change across the line and little along it. A ridge
//     doesn't work there: the paver joints are ridges too, and the arm alternates white and
//     red. Found at 640 x 360. On 10-24's kept frames (7,770): journal down 1,413, the line
//     at least 0.6 in 1,396; journal up 1,070, below 0.5 in 1,068.
//
// The arm is down when the line is covered, broken when its pivot half is covered and its
// outer half isn't. Watch only for now: each settled state is journaled as "arm_line", and a
// broken arm lasting the lane's arm alert wait as "arm_line_alert", without an alert.
const (
	armLineRidge = "ridge"
	armLineEdges = "edges"

	armLineDown      = 0.6 // the arm is down when this share of the line is covered (both cameras)
	armLinePivotMin  = 0.8 // and broken when this share of its pivot half is
	armLineOuterMax  = 0.4 // and less than this of its outer half
	armLineThreshold = 6.0 // a point is covered at this contrast (of 255)
)

// armLineSearch is where a kind looks around the region's ends, in percent of the picture:
// the pivot end's x, the tip end's x and the tip end's y. As in the prototypes the numbers
// were found with (2026-10-24).
type armLineSearch struct {
	w, h               int
	pivotX, tipX, tipY []float64
}

var armLineSearches = map[string]armLineSearch{
	armLineRidge: {w: 480, h: 270, pivotX: []float64{-1.5, -0.5, 0.5, 1.5}, tipX: []float64{-2, -1, 0, 1, 2}, tipY: []float64{0}},
	armLineEdges: {w: 640, h: 360, pivotX: []float64{-4, -2, 0, 2, 4}, tipX: []float64{-2, 0, 2}, tipY: []float64{-4, -2, 0, 2, 4}},
}

func isArmLine(kind string) bool { return kind == armLineRidge || kind == armLineEdges }

// armLineScore is the best line's cover: the whole line, its pivot half and its outer half.
type armLineScore struct {
	cover, pivot, outer float64
}

func (s armLineScore) state() string {
	switch {
	case s.pivot >= armLinePivotMin && s.outer < armLineOuterMax:
		return "broken"
	case s.cover >= armLineDown:
		return "down"
	}
	return "not_down" // up, moving, or hidden by a vehicle
}

// scoreArmLine finds the lowered arm's line in a frame. Frames of any size are scaled to the
// kind's working size first (the Reolink now and then answers with its full 3840 x 2160).
func scoreArmLine(img image.Image, r MotionRegion) armLineScore {
	s, ok := armLineSearches[r.Kind]
	if !ok {
		return armLineScore{}
	}
	g := blur3(grayResize(img, s.w, s.h), s.w, s.h)
	var gx, gy []float32
	if r.Kind == armLineEdges {
		gx, gy = gradients(g, s.w, s.h)
	}
	y0 := math.Min(r.Bottom/100*float64(s.h), float64(s.h-1))
	var best armLineScore
	first := true
	for _, dp := range s.pivotX {
		for _, dx := range s.tipX {
			for _, dy := range s.tipY {
				x0 := (r.Left + dp) / 100 * float64(s.w)
				x1, y1 := (r.Right+dx)/100*float64(s.w), (r.Top+dy)/100*float64(s.h)
				var flags []bool
				if r.Kind == armLineEdges {
					flags = edgeFlags(gx, gy, s.w, s.h, x0, y0, x1, y1)
				} else {
					flags = ridgeFlags(g, s.w, s.h, x0, y0, x1, y1)
				}
				sc := armLineScore{cover: share(flags), pivot: share(flags[:len(flags)/2]), outer: share(flags[len(flags)/2:])}
				if first || sc.cover > best.cover {
					best, first = sc, false
				}
			}
		}
	}
	return best
}

func share(flags []bool) float64 {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	if len(flags) == 0 {
		return 0
	}
	return float64(n) / float64(len(flags))
}

// ridgeFlags tells, at 60 points along the line, whether the line is a ridge there: brighter
// (or darker) than both sides by armLineThreshold, at the best of a few widths.
func ridgeFlags(g []float32, w, h int, x0, y0, x1, y1 float64) []bool {
	const n = 60
	L := math.Hypot(x1-x0, y1-y0)
	nx, ny := -(y1-y0)/L, (x1-x0)/L
	flags := make([]bool, n)
	for i := range flags {
		t := float64(i) / (n - 1)
		x, y := x0+(x1-x0)*t, y0+(y1-y0)*t
		c := sampleAt(g, w, h, x, y)
		best := float32(math.Inf(-1))
		for _, d := range []float64{2, 3, 4, 6} {
			l, r := sampleAt(g, w, h, x-nx*d, y-ny*d), sampleAt(g, w, h, x+nx*d, y+ny*d)
			best = max(best, min(c-l, c-r), min(l-c, r-c))
		}
		flags[i] = best >= armLineThreshold
	}
	return flags
}

// edgeFlags tells, at 80 points along the line (leaving out 3% at each end), whether the arm's
// two borders are there: at the best of a few half-widths, a brightness change at both sides of
// the line, across it more than twice as strong as along it.
func edgeFlags(gx, gy []float32, w, h int, x0, y0, x1, y1 float64) []bool {
	const n = 80
	L := math.Hypot(x1-x0, y1-y0)
	ux, uy := (x1-x0)/L, (y1-y0)/L
	nx, ny := -uy, ux
	flags := make([]bool, n)
	for i := range flags {
		t := 0.03 + 0.94*float64(i)/(n-1)
		x, y := x0+(x1-x0)*t, y0+(y1-y0)*t
		var best float64
		for _, d := range []float64{3, 4, 5, 6, 8} {
			side := func(sgn float64) float64 {
				px, py := x+sgn*nx*d, y+sgn*ny*d
				ax, ay := float64(sampleAt(gx, w, h, px, py)), float64(sampleAt(gy, w, h, px, py))
				perp, along := math.Abs(ax*nx+ay*ny), math.Abs(ax*ux+ay*uy)
				if perp > 2*along {
					return perp
				}
				return 0
			}
			best = math.Max(best, math.Min(side(-1), side(1)))
		}
		flags[i] = best >= armLineThreshold
	}
	return flags
}

// sampleAt is the nearest pixel, clamped to the picture.
func sampleAt(g []float32, w, h int, x, y float64) float32 {
	xi := min(max(int(math.Round(x)), 0), w-1)
	yi := min(max(int(math.Round(y)), 0), h-1)
	return g[yi*w+xi]
}

// grayResize is the picture's brightness at w x h, each pixel the average of the source pixels
// it covers.
func grayResize(img image.Image, w, h int) []float32 {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	ycc, isYCC := img.(*image.YCbCr)
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		sy0, sy1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			sx0, sx1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			sum, n := 0, 0
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					px, py := b.Min.X+sx, b.Min.Y+sy
					if isYCC {
						sum += int(ycc.Y[ycc.YOffset(px, py)])
					} else {
						sum += int(color.GrayModel.Convert(img.At(px, py)).(color.Gray).Y)
					}
					n++
				}
			}
			out[y*w+x] = float32(sum) / float32(n)
		}
	}
	return out
}

// blur3 is a 3 x 3 box blur (against the infrared grain), the edges repeated.
func blur3(g []float32, w, h int) []float32 {
	out := make([]float32, len(g))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float32
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					sum += g[min(max(y+dy, 0), h-1)*w+min(max(x+dx, 0), w-1)]
				}
			}
			out[y*w+x] = sum / 9
		}
	}
	return out
}

// gradients are the brightness changes across x and y (central differences; 0 at the border).
func gradients(g []float32, w, h int) (gx, gy []float32) {
	gx, gy = make([]float32, len(g)), make([]float32, len(g))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if x > 0 && x < w-1 {
				gx[i] = (g[i+1] - g[i-1]) / 2
			}
			if y > 0 && y < h-1 {
				gy[i] = (g[i+w] - g[i-w]) / 2
			}
		}
	}
	return gx, gy
}

// armLineChange is a line region's newly settled state, for the journal.
type armLineChange struct {
	name, state string
	score       armLineScore
	minOuter    float64   // the lowest outer half over the "down" stretch that just ended; -1: none
	alert       string    // "arm_line_alert" or "arm_line_ok" with this change; "": neither
	since       time.Time // the broken stretch's start, with an alert
}

// lineStates scores each line region and returns the ones whose state settled on a new value
// (after brightSettleFrames agree), and a broken arm that has lasted `after`.
func (d *motionDetector) lineStates(t time.Time, img image.Image, after time.Duration) []armLineChange {
	var changes []armLineChange
	for _, r := range d.regions {
		if !isArmLine(r.Kind) {
			continue
		}
		sc := scoreArmLine(img, r.MotionRegion)
		state := sc.state()
		if r.line == "down" && state == "down" {
			r.minOuter = math.Min(r.minOuter, sc.outer)
		}
		if r.line == "broken" && !r.lineAlerted && t.Sub(r.brokenSince) >= after {
			r.lineAlerted = true
			changes = append(changes, armLineChange{name: r.Name, state: "broken", score: sc, minOuter: -1, alert: "arm_line_alert", since: r.brokenSince})
		}
		if state == r.line {
			r.agree = 0
			continue
		}
		if r.agree++; r.agree < brightSettleFrames {
			continue
		}
		c := armLineChange{name: r.Name, state: state, score: sc, minOuter: -1}
		if r.line == "down" {
			c.minOuter = r.minOuter
		}
		if state == "down" {
			r.minOuter = sc.outer
			if r.lineAlerted {
				c.alert, c.since, r.lineAlerted = "arm_line_ok", r.brokenSince, false
			}
		}
		if state == "broken" {
			r.brokenSince = t
		}
		r.line, r.agree = state, 0
		changes = append(changes, c)
	}
	return changes
}
