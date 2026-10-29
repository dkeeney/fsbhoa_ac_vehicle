package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestFrontPlateLive reads front plates of past cars from the NVR, with the installed reader
// and the NVR settings in the service's config: FRONT_PLATE_CARS is lines of
// "<camera IP> <YYYY-MM-DD HH:MM:SS.mmm when it entered beyond> [expected plate]". Reads the
// production NVR (only reads it).
func TestFrontPlateLive(t *testing.T) {
	cars := os.Getenv("FRONT_PLATE_CARS")
	if cars == "" {
		t.Skip("FRONT_PLATE_CARS not set")
	}
	cfg, err := LoadConfig("/var/lib/fsbhoa/vehicle_service.json")
	if err != nil {
		t.Fatal(err)
	}
	nvr := NewNVRVideo()
	nvr.SetConfig(cfg)
	f := NewFrontPlates(nvr, nil, nil)
	if !f.installed() {
		t.Skip("reader not installed in " + frontPlateDir)
	}
	for _, line := range strings.Split(strings.TrimSpace(cars), "\n") {
		w := strings.Fields(line)
		if len(w) < 3 {
			continue
		}
		start, err := time.ParseInLocation("2006-01-02 15:04:05.000", w[1]+" "+w[2], time.Local)
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		p, err := f.read(unreadCar{camera: w[0], gate: "test", start: start})
		t.Logf("%s %s: %q %.2f (%d of %d frames, crop %d bytes) in %v, err %v", w[0], w[2], p.Plate, p.Confidence, p.Read, p.Frames, len(p.crop), time.Since(began).Round(time.Second), err)
		if len(w) > 3 && p.Plate != w[3] {
			t.Errorf("%s: want %q", w[2], w[3])
		}
	}
}
