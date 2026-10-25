package main

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// armFrame draws a w x h picture of noisy pavers with a white arm, 6 px wide at 640 wide, from
// (x0, y0) to (x1, y1) in percent, drawn from its pivot end for `drawn` of its length.
func armFrame(w, h int, x0, y0, x1, y1, drawn float64) image.Image {
	img := image.NewGray(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	ax, ay := x0/100*float64(w), math.Min(y0/100*float64(h), float64(h-1))
	bx, by := x1/100*float64(w), y1/100*float64(h)
	L := math.Hypot(bx-ax, by-ay)
	half := 3 * float64(w) / 640
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 90 + rng.Intn(9)
			// distance from the arm's line, and how far along it
			t := ((float64(x)-ax)*(bx-ax) + (float64(y)-ay)*(by-ay)) / (L * L)
			d := math.Abs((float64(x)-ax)*(by-ay)-(float64(y)-ay)*(bx-ax)) / L
			if t >= 0 && t <= drawn && d <= half {
				v = 220
			}
			img.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return img
}

func TestArmLineStates(t *testing.T) {
	for _, c := range []struct {
		kind  string
		w, h  int
		drawn float64
		want  string
	}{
		{armLineRidge, 1920, 1080, 1, "down"},
		{armLineRidge, 1920, 1080, 0.45, "broken"},
		{armLineRidge, 1920, 1080, 0, "not_down"},
		{armLineEdges, 640, 360, 1, "down"},
		{armLineEdges, 640, 360, 0.45, "broken"},
		{armLineEdges, 640, 360, 0, "not_down"},
		{armLineEdges, 1920, 1080, 1, "down"}, // the Reolink's odd full-size answer, scaled
	} {
		r := MotionRegion{Name: "arm-line", Kind: c.kind, Left: 30, Top: 55, Right: 70, Bottom: 100}
		sc := scoreArmLine(armFrame(c.w, c.h, r.Left, r.Bottom, r.Right, r.Top, c.drawn), r)
		if got := sc.state(); got != c.want {
			t.Errorf("%s %dx%d drawn %.2f: %s (cover %.2f, pivot %.2f, outer %.2f), want %s", c.kind, c.w, c.h, c.drawn, got, sc.cover, sc.pivot, sc.outer, c.want)
		}
	}
}

// A state settles after two frames agree; a broken arm lasting the wait is reported once, and
// the whole arm down again ends it.
func TestArmLineSettleAndAlert(t *testing.T) {
	r := MotionRegion{Name: "arm-line", Kind: armLineEdges, Left: 30, Top: 55, Right: 70, Bottom: 100}
	d := newMotionDetector([]MotionRegion{r})
	down, broken := armFrame(640, 360, 30, 100, 70, 55, 1), armFrame(640, 360, 30, 100, 70, 55, 0.45)
	t0 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	var got []string
	for i, f := range []image.Image{down, down, broken, down, broken, broken, broken, broken, broken, down, down} {
		for _, c := range d.lineStates(t0.Add(time.Duration(i)*time.Second), f, 3*time.Second) {
			got = append(got, strings.TrimSpace(c.state+" "+c.alert))
		}
	}
	want := []string{"down", "broken", "broken arm_line_alert", "down arm_line_ok"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestArmLineReplay prints each recorded frame's arm line score and a summary. It runs only
// with ARM_LINE_DIR set to a folder of frames and ARM_LINE_REGION to the region as in the
// Motion Regions setting, without the camera and name ("44.5 38 58 100 ridge"):
//
//	ARM_LINE_DIR=/path/frames ARM_LINE_REGION="28 59 70 100 edges" go test -run ArmLineReplay -v
func TestArmLineReplay(t *testing.T) {
	dir := os.Getenv("ARM_LINE_DIR")
	if dir == "" {
		t.Skip("ARM_LINE_DIR not set")
	}
	r := MotionRegion{Name: "arm-line"}
	if n, _ := fmt.Sscan(os.Getenv("ARM_LINE_REGION"), &r.Left, &r.Top, &r.Right, &r.Bottom, &r.Kind); n != 5 {
		t.Fatal("ARM_LINE_REGION: left top right bottom kind")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	sort.Strings(files)
	counts := map[string]int{}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		img, err := jpeg.Decode(fh)
		fh.Close()
		if err != nil {
			continue
		}
		sc := scoreArmLine(img, r)
		counts[sc.state()]++
		fmt.Printf("%s %s %.2f %.2f %.2f\n", filepath.Base(f), sc.state(), sc.cover, sc.pivot, sc.outer)
	}
	fmt.Println("frames", len(files), counts)
}

func BenchmarkArmLine(b *testing.B) {
	for _, c := range []struct {
		kind string
		w, h int
	}{{armLineRidge, 1920, 1080}, {armLineEdges, 640, 360}} {
		r := MotionRegion{Name: "arm-line", Kind: c.kind, Left: 30, Top: 55, Right: 70, Bottom: 100}
		img := armFrame(c.w, c.h, 30, 100, 70, 55, 1)
		b.Run(c.kind, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scoreArmLine(img, r)
			}
		})
	}
}
