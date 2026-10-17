package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testFrame is a 320 x 240 JPEG of one brightness, with a darker box (a "car") at x, or none
// when x < 0.
func testFrame(t *testing.T, base uint8, x int) []byte {
	t.Helper()
	img := image.NewYCbCr(image.Rect(0, 0, 320, 240), image.YCbCrSubsampleRatio420)
	for i := range img.Y {
		img.Y[i] = base
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = 128, 128
	}
	if x >= 0 {
		for py := 80; py < 160; py++ {
			for px := x; px < x+60 && px < 320; px++ {
				img.Y[img.YOffset(px, py)] = base / 4
			}
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRegionShare(t *testing.T) {
	gray := func(base uint8, x int) *grayFrame {
		img, err := jpeg.Decode(bytes.NewReader(testFrame(t, base, x)))
		if err != nil {
			t.Fatal(err)
		}
		return reduceGray(img, motionScale)
	}
	whole := MotionRegion{Name: "all", Left: 0, Top: 0, Right: 100, Bottom: 100}
	left := MotionRegion{Name: "left", Left: 0, Top: 0, Right: 25, Bottom: 100}

	if s := regionShare(gray(150, -1), gray(190, -1), whole); s != 0 {
		t.Errorf("a brightness change alone: share %v, want 0", s)
	}
	if s := regionShare(gray(150, -1), gray(150, 200), whole); s < motionShare {
		t.Errorf("a car appearing: share %v, want at least %v", s, motionShare)
	}
	if s := regionShare(gray(150, -1), gray(170, 200), whole); s < motionShare {
		t.Errorf("a car appearing as the light changes: share %v, want at least %v", s, motionShare)
	}
	if s := regionShare(gray(150, -1), gray(150, 200), left); s != 0 {
		t.Errorf("a car outside the region: share %v, want 0", s)
	}
}

func TestMotionPass(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMotion(filepath.Join(dir, "frames"), 3, journal, t.Logf)
	m.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.31"}}, nil))
	w := m.newWatch("192.0.2.31", []MotionRegion{
		{Camera: "192.0.2.31", Name: "gate", Left: 50, Top: 0, Right: 100, Bottom: 100},
		{Camera: "192.0.2.31", Name: "arm", Left: 0, Top: 0, Right: 10, Bottom: 10},
	})

	// Still frames, a car crossing the gate region for 3 frames, then still again.
	t0 := time.Date(2026, 10, 17, 14, 0, 0, 0, time.Local)
	xs := []int{-1, -1, -1, -1, -1, 170, 210, 250, -1, -1, -1, -1, -1, -1, -1, -1}
	for i, x := range xs {
		w.handleFrame(t0.Add(time.Duration(i)*time.Second), testFrame(t, 150, x))
	}
	if w.pass != nil {
		t.Fatal("the pass didn't end")
	}

	f, err := os.Open(filepath.Join(dir, "journal", "journal-2026-10-17.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var passes []map[string]interface{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e["kind"] == "motion" {
			passes = append(passes, e)
		}
	}
	if len(passes) != 1 {
		t.Fatalf("got %d motion entries, want 1", len(passes))
	}
	p := passes[0]
	if p["gate"] != "South Enter" || p["device"] != "overview" {
		t.Errorf("lane: got %v %v", p["gate"], p["device"])
	}
	regions := p["regions"].([]interface{})
	if len(regions) != 1 || regions[0].(map[string]interface{})["region"] != "gate" {
		t.Errorf("regions: got %v, want only gate", regions)
	}
	r := regions[0].(map[string]interface{})
	if r["start"] != t0.Add(5*time.Second).Format(journalTimeFormat) || r["end"] != t0.Add(8*time.Second).Format(journalTimeFormat) {
		t.Errorf("gate motion %v to %v, want 14:00:05 to 14:00:08", r["start"], r["end"])
	}
	// 3 frames before, the 4 that changed, 3 still ones until the region ends, 2 after
	frames := p["frames"].([]interface{})
	if len(frames) != 12 {
		t.Errorf("got %d frames, want 12: %v", len(frames), frames)
	}
	first := filepath.Join(dir, "frames", frames[0].(string))
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 120 {
		t.Errorf("saved frame is %v, want half size", img.Bounds())
	}
	if frames[0] != filepath.Join("2026-10-17", "192.0.2.31", "140002.000.jpg") {
		t.Errorf("first frame %v, want the one 3 seconds before the motion", frames[0])
	}
}

func TestBrightRegion(t *testing.T) {
	d := newMotionDetector([]MotionRegion{{Name: "arm-up", Kind: "bright", Left: 40, Top: 0, Right: 60, Bottom: 100}})
	frame := func(armUp bool) image.Image {
		img := image.NewYCbCr(image.Rect(0, 0, 100, 100), image.YCbCrSubsampleRatio420)
		for i := range img.Y {
			img.Y[i] = 60 // the hedge
		}
		if armUp {
			for y := 0; y < 100; y++ {
				for x := 48; x < 53; x++ {
					img.Y[img.YOffset(x, y)] = 230 // the white arm, a quarter of the region
				}
			}
		}
		return img
	}
	t0 := time.Date(2026, 10, 17, 14, 0, 0, 0, time.Local)
	var got []string
	for i, up := range []bool{false, false, false, true, false, true, true, true, false, false} {
		for _, c := range d.brightStates(t0.Add(time.Duration(i)*time.Second), frame(up)) {
			got = append(got, fmt.Sprintf("%d:%v", i, c.on))
		}
	}
	// settles down at frame 1; one up frame alone doesn't count; up at 6; down at 9
	want := []string{"1:false", "6:true", "9:false"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("changes %v, want %v", got, want)
	}
	if d.moving() {
		t.Error("a bright region counted as motion")
	}
}
