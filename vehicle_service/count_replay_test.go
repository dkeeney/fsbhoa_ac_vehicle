package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestCountReplay runs recorded frames through the car counter and prints what it counts.
// It runs only with COUNT_REPLAY_DIR set to a folder of frames named by their local time of
// day (HHMMSS.mmm.jpg, as recorded for the zones on 2026-10-20), with the zones in
// COUNT_REPLAY_ZONES, two lines in the Car Count Zones setting's format without the camera:
//
//	COUNT_REPLAY_DIR=/path/frames COUNT_REPLAY_ZONES="approach 18 100 ...
//	beyond 42 100 ..." go test -run CountReplay -v
func TestCountReplay(t *testing.T) {
	dir := os.Getenv("COUNT_REPLAY_DIR")
	if dir == "" {
		t.Skip("COUNT_REPLAY_DIR not set")
	}
	// Lines as in the setting, without the camera: "approach|beyond <x> <y> ... [on=<share>] [window=<s>]"
	var zones []CountZone
	for _, line := range strings.Split(os.Getenv("COUNT_REPLAY_ZONES"), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		zone := CountZone{Camera: "replay", Zone: f[0]}
		var nums []float64
		for _, w := range f[1:] {
			if k, v, ok := strings.Cut(w, "="); ok {
				var x float64
				fmt.Sscan(v, &x)
				if k == "on" {
					zone.On = x
				} else if k == "window" {
					zone.Window = x
				}
				continue
			}
			var x float64
			fmt.Sscan(w, &x)
			nums = append(nums, x)
		}
		for i := 0; i+1 < len(nums); i += 2 {
			zone.Points = append(zone.Points, [2]float64{nums[i], nums[i+1]})
		}
		zones = append(zones, zone)
	}
	c := NewCarCounter(nil, t.Logf)
	c.SetZones(zones)
	z := c.cameras["replay"]
	if z == nil {
		t.Fatal("COUNT_REPLAY_ZONES needs an approach and a beyond line")
	}

	names, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	sort.Strings(names)
	day := time.Now()
	counts := map[string]int{}
	for _, name := range names {
		at, err := time.ParseInLocation("150405.000", strings.TrimSuffix(filepath.Base(name), ".jpg"), time.Local)
		if err != nil {
			continue
		}
		at = time.Date(day.Year(), day.Month(), day.Day(), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), time.Local)
		data, err := os.ReadFile(name)
		if err != nil || len(data) < 1000 {
			continue
		}
		img, err := decodeJPEG(data)
		if err != nil {
			continue
		}
		for _, e := range z.step(at, countGray(img)) {
			counts[e.kind]++
			t.Logf("%s %v", e.kind, e.data)
		}
	}
	t.Logf("%d frames: %v", len(names), counts)
}
