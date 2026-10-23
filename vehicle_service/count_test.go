package main

import (
	"testing"
	"time"
)

// countFrame is a plain frame with a dark "car" covering rows y0 to y1 of 90 (none if y0 >= y1).
func countFrame(y0, y1 int) []uint8 {
	g := make([]uint8, countWidth*countHeight)
	for i := range g {
		g[i] = 120
	}
	y0, y1 = y0*countHeight/90, y1*countHeight/90
	for y := y0; y < y1 && y < countHeight; y++ {
		for x := 0; x < countWidth; x++ {
			g[y*countWidth+x] = 20
		}
	}
	return g
}

// The picture's top half is "approach", the bottom half "beyond"; a car moves down the picture.
func newTestCounter() *zoneCounter {
	return &zoneCounter{
		approach: polygonMask([][2]float64{{0, 0}, {100, 0}, {100, 50}, {0, 50}}),
		beyond:   polygonMask([][2]float64{{0, 50}, {100, 50}, {100, 100}, {0, 100}}),
		aOn:      countOnShare,
		bOn:      countOnShare,
		window:   countApproachWindow,
		jump:     countExposureJump,
	}
}

func runCounter(z *zoneCounter, rows [][2]int) map[string]int {
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	counts := map[string]int{}
	for i := 0; i < 30; i++ { // a still lane to start the background
		z.step(t0.Add(time.Duration(i)*500*time.Millisecond), countFrame(0, 0))
	}
	for i, r := range rows {
		for _, e := range z.step(t0.Add(time.Duration(30+i)*500*time.Millisecond), countFrame(r[0], r[1])) {
			counts[e.kind]++
		}
	}
	return counts
}

func TestCountCarIn(t *testing.T) {
	// Into the approach, across, beyond, out of the picture
	rows := [][2]int{{0, 20}, {5, 40}, {20, 60}, {40, 80}, {55, 90}, {70, 90}, {0, 0}, {0, 0}, {0, 0}}
	if got := runCounter(newTestCounter(), rows); got["car"] != 1 || len(got) != 1 {
		t.Fatalf("counted %v, want one car", got)
	}
}

func TestCountCarBacked(t *testing.T) {
	// Into the approach, stops, backs out the way it came
	rows := [][2]int{{0, 20}, {5, 40}, {10, 44}, {10, 44}, {5, 40}, {0, 20}}
	if got := runCounter(newTestCounter(), rows); len(got) != 0 {
		t.Fatalf("counted %v before the window passed, want nothing yet", got)
	}
	rows = append(rows, make([][2]int, 20)...) // 10 s of an empty lane: past the 8 s window
	if got := runCounter(newTestCounter(), rows); got["car_backed"] != 1 || len(got) != 1 {
		t.Fatalf("counted %v, want one car backed out", got)
	}
}

// A car that reaches into "beyond" at the arm and reverses (2026-10-22 13:08) backed out: it
// leaves "beyond" toward "approach", not out the far side.
func TestCountCarWentBack(t *testing.T) {
	rows := [][2]int{{0, 20}, {5, 40}, {20, 70}, {30, 75}, {30, 75}, {20, 62}, {10, 52}, {0, 20}}
	rows = append(rows, make([][2]int, 20)...)
	if got := runCounter(newTestCounter(), rows); got["car_backed"] != 1 || len(got) != 1 {
		t.Fatalf("counted %v, want one car backed out", got)
	}
}

// A car that waits between the zones (North Enter: the keypad, then the swing gate) is still
// a car in when its zone's window covers the wait.
func TestCountSlowCarWithWindow(t *testing.T) {
	rows := [][2]int{{0, 20}, {5, 40}, {5, 40}, {0, 0}}
	rows = append(rows, make([][2]int, 24)...) // 12 s out of both zones
	rows = append(rows, [2]int{60, 90}, [2]int{60, 90}, [2]int{0, 0}, [2]int{0, 0})
	if got := runCounter(newTestCounter(), rows); got["car_backed"] != 1 || got["beyond_only"] != 1 {
		t.Fatalf("with the 8 s window: counted %v, want a car backed out and a beyond_only", got)
	}
	z := newTestCounter()
	z.window = 20 * time.Second
	if got := runCounter(z, rows); got["car"] != 1 || len(got) != 1 {
		t.Fatalf("with a 20 s window: counted %v, want one car", got)
	}
}

func TestCountBeyondOnly(t *testing.T) {
	// Something only past the arm
	rows := [][2]int{{60, 90}, {60, 90}, {60, 90}, {0, 0}, {0, 0}, {0, 0}}
	if got := runCounter(newTestCounter(), rows); got["beyond_only"] != 1 || len(got) != 1 {
		t.Fatalf("counted %v, want one beyond_only", got)
	}
}

func TestCountTwoCars(t *testing.T) {
	// Two cars, with the lane clear beyond the arm between them
	one := [][2]int{{0, 20}, {5, 40}, {20, 60}, {40, 80}, {55, 90}, {70, 90}, {0, 0}, {0, 0}}
	if got := runCounter(newTestCounter(), append(append([][2]int{}, one...), one...)); got["car"] != 2 || len(got) != 1 {
		t.Fatalf("counted %v, want two cars", got)
	}
}

func TestPolygonMask(t *testing.T) {
	m := polygonMask([][2]float64{{0, 0}, {100, 0}, {0, 100}}) // the top-left half
	if !m[1*blocksW+1] || m[(blocksH-2)*blocksW+blocksW-2] {
		t.Fatal("triangle mask wrong")
	}
}

// A long vehicle whose front reaches "beyond" before "approach" fills (2026-10-20 12:31, a
// truck towing a wood chipper) isn't also a car backing out.
func TestCountLongVehicle(t *testing.T) {
	rows := [][2]int{{55, 90}, {10, 95}, {0, 100}, {0, 100}, {0, 100}, {0, 60}, {0, 30}, {0, 0}}
	rows = append(rows, make([][2]int, 20)...) // past the approach window
	if got := runCounter(newTestCounter(), rows); got["car_backed"] != 0 || got["beyond_only"] != 1 {
		t.Fatalf("counted %v, want only a beyond_only (a car once the arm says it was up)", got)
	}
}

// "beyond" filling while the lane's arm is up is a car, with no "approach" before it.
func TestCountBeyondWithArmUp(t *testing.T) {
	for _, up := range []bool{true, false} {
		c := NewCarCounter(nil, t.Logf)
		var got []string
		c.SetArm(func(string, time.Time) (string, []byte) { return "", nil }, func(string, time.Time) bool { return up }, nil)
		c.gates.Store(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
		zc := newTestCounter()
		t0 := time.Date(2026, 10, 21, 15, 1, 0, 0, time.Local)
		frames := append(make([][2]int, 30), [2]int{60, 90}, [2]int{60, 90}, [2]int{0, 0}, [2]int{0, 0}, [2]int{0, 0})
		for i, r := range frames {
			for _, e := range zc.step(t0.Add(time.Duration(i)*500*time.Millisecond), countFrame(r[0], r[1])) {
				e = c.classify("192.0.2.41", e)
				got = append(got, e.kind)
			}
		}
		want := "beyond_only"
		if up {
			want = "car"
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("arm up %v: got %v, want %s", up, got, want)
		}
	}
}
