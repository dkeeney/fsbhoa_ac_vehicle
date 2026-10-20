package main

import (
	"testing"
	"time"
)

// countFrame is a plain frame with a dark "car" covering rows y0 to y1 (none if y0 >= y1).
func countFrame(y0, y1 int) []uint8 {
	g := make([]uint8, countWidth*countHeight)
	for i := range g {
		g[i] = 120
	}
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
	rows := [][2]int{{0, 20}, {5, 40}, {10, 44}, {10, 44}, {5, 40}, {0, 20}, {0, 0}, {0, 0}, {0, 0}}
	if got := runCounter(newTestCounter(), rows); got["car_backed"] != 1 || len(got) != 1 {
		t.Fatalf("counted %v, want one car backed out", got)
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
	if !m[1*countWidth+1] || m[(countHeight-2)*countWidth+countWidth-2] {
		t.Fatal("triangle mask wrong")
	}
}
