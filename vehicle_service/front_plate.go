package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FrontPlates reads the front plate of a car the LPR camera didn't read (unread.go), from the
// overview camera's recording on the NVR (CLAUDE.md, "Front plates from the NVR"; 2026-10-29,
// the user's decision after a test). The LPR cameras read the rear plate; the overview camera
// sees the car front on. On 14 unread cars at South Enter, .141's 4K recording gave the front
// plate of every daytime car that had one (6, two of them a Corolla whose rear plate is behind
// a bike rack), two decorative front plates, two cars with none; at night, motion blur and
// headlights left nothing readable, so only daytime cars are tried (the overview photo has
// color).
//
// The frames come from the NVR (NVRVideo.Frames: frontPlateBefore before the car entered
// "beyond" to frontPlateAfter after it, frontPlateFPS a second, full size), and are read by
// tools/front_plate.py (fast-alpr: a plate detector and OCR, ONNX), installed with its Python
// in frontPlateDir by tools/install_front_plate.sh. Without it, nothing is tried.
type FrontPlates struct {
	nvr     *NVRVideo
	photos  *OverviewPhotos
	journal *Journal
	one     chan struct{} // one car at a time: the reader takes the Pi's four cores
}

const (
	frontPlateDir     = "/opt/fsbhoa/alpr" // bin/python and front_plate.py
	frontPlateBefore  = 3 * time.Second
	frontPlateAfter   = 1 * time.Second
	frontPlateFPS     = 2
	frontPlateTimeout = 2 * time.Minute // the NVR (up to about 20 s) and the reader (up to about 30 s)
)

// frontPlate is a front plate read from the video.
type frontPlate struct {
	Plate      string  `json:"plate"`
	Confidence float64 `json:"confidence"`
	Frames     int     `json:"frames"` // frames read
	Read       int     `json:"read"`   // frames that read the plate
	crop       []byte
}

func NewFrontPlates(nvr *NVRVideo, photos *OverviewPhotos, journal *Journal) *FrontPlates {
	return &FrontPlates{nvr: nvr, photos: photos, journal: journal, one: make(chan struct{}, 1)}
}

// installed reports whether the reader is installed.
func (f *FrontPlates) installed() bool {
	for _, p := range []string{"bin/python", "front_plate.py"} {
		if _, err := os.Stat(filepath.Join(frontPlateDir, p)); err != nil {
			return false
		}
	}
	return true
}

// daytime reports whether the camera's frame at t has color: not infrared.
func (f *FrontPlates) daytime(camera string, t time.Time) bool {
	if f.photos == nil {
		return false
	}
	b, _ := f.photos.Near(camera, t)
	if b == nil {
		return false
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	return err == nil && !colorless(img)
}

// Read reads the car's front plate, and journals the attempt ("front_plate"). False when it
// wasn't tried (night, no reader, no NVR) or no plate read.
func (f *FrontPlates) Read(c unreadCar) (frontPlate, bool) {
	if f == nil || !f.installed() || !f.daytime(c.camera, c.start) {
		return frontPlate{}, false
	}
	f.one <- struct{}{}
	defer func() { <-f.one }()
	began := time.Now()
	p, err := f.read(c)
	entry := map[string]interface{}{
		"camera_ip": c.camera, "gate": c.gate, "beyond_start": c.start.Local().Format(journalTimeFormat),
		"plate": p.Plate, "confidence": p.Confidence, "frames": p.Frames, "read": p.Read,
		"seconds": time.Since(began).Round(100 * time.Millisecond).Seconds(),
	}
	if err != nil {
		entry["error"] = err.Error()
		log.Printf("[FRONT] %s %s: %v", c.gate, c.start.Local().Format("15:04:05"), err)
	} else if p.Plate != "" {
		log.Printf("[FRONT] %s %s: front plate %q (%.2f, %d of %d frames)", c.gate, c.start.Local().Format("15:04:05"), p.Plate, p.Confidence, p.Read, p.Frames)
	}
	if f.journal != nil {
		f.journal.Write("front_plate", entry)
	}
	return p, err == nil && p.Plate != ""
}

func (f *FrontPlates) read(c unreadCar) (frontPlate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), frontPlateTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "front_plate_")
	if err != nil {
		return frontPlate{}, err
	}
	defer os.RemoveAll(dir)
	if err := f.nvr.Frames(ctx, c.camera, c.start.Add(-frontPlateBefore), frontPlateBefore+frontPlateAfter, frontPlateFPS, dir); err != nil {
		return frontPlate{}, fmt.Errorf("NVR: %w", err)
	}
	frames, _ := filepath.Glob(filepath.Join(dir, "f*.jpg"))
	if len(frames) == 0 {
		return frontPlate{}, fmt.Errorf("the NVR sent no frames")
	}
	crop := filepath.Join(dir, "plate.jpg")
	cmd := exec.CommandContext(ctx, filepath.Join(frontPlateDir, "bin/python"), append([]string{"-I", filepath.Join(frontPlateDir, "front_plate.py"), crop}, frames...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return frontPlate{}, fmt.Errorf("reader: %v %.300s", err, stderr.String())
	}
	var p frontPlate
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n")) // the JSON is the last line
	if err := json.Unmarshal(lines[len(lines)-1], &p); err != nil {
		return frontPlate{}, fmt.Errorf("reader said %.200q: %v", out, err)
	}
	if p.Plate != "" {
		p.crop, _ = os.ReadFile(crop)
	}
	return p, nil
}
