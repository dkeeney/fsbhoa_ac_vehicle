package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJPEG writes a w x h test photo.
func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPlateEvents(t *testing.T) {
	queue, photos := t.TempDir(), t.TempDir()
	disp, err := NewDispatcher(&ServiceConfig{QueueDir: queue})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPlateEvents(disp, false)

	plateFile, sceneFile := filepath.Join(photos, "plate.jpg"), filepath.Join(photos, "src.jpg")
	writeJPEG(t, plateFile, 160, 60)
	writeJPEG(t, sceneFile, 2592, 1520)
	read := PlateRead{
		Camera: "5c-f2-07-5f-fd-be", CameraName: "S Gate Exit LPR", CameraIP: "192.168.107.137",
		Gate: "South", Direction: "exit", Plate: "8RGW713", CaptureTime: "2026-10-17-08-01-22-841",
		PlateFile: plateFile, SceneFile: sceneFile, PlateArrived: time.Now(), SceneArrived: time.Now(),
	}

	if p.Send(read) {
		t.Fatal("queued an event while off")
	}
	p.SetEnabled(true)
	if !p.Send(read) {
		t.Fatal("no event queued")
	}

	data, err := os.ReadFile(filepath.Join(queue, "lpr_5c-f2-07-5f-fd-be_2026-10-17-08-01-22-841.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]interface{}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev["event_timestamp"] != "2026-10-17 08:01:22.841" || ev["gate_identifier"] != "South" || ev["direction"] != "exit" ||
		ev["lpr_plate"] != "8RGW713" || ev["auth_id"] != nil {
		t.Errorf("event: %v", ev)
	}
	if d, _ := ev["raw_details"].(map[string]interface{}); d["source"] != "plate_only" || d["camera"] != "5c-f2-07-5f-fd-be" {
		t.Errorf("raw_details: %v", ev["raw_details"])
	}

	// The scene photo is a third of the size each way; the plate crop is sent as it is
	scene, _ := base64.StdEncoding.DecodeString(ev["context_image_b64"].(string))
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(scene)); err != nil || cfg.Width != 864 || cfg.Height != 506 {
		t.Errorf("scene photo: %v %v", cfg, err)
	}
	crop, _ := base64.StdEncoding.DecodeString(ev["lpr_image_b64"].(string))
	if orig, _ := os.ReadFile(plateFile); !bytes.Equal(crop, orig) {
		t.Error("plate crop changed")
	}

	// A vehicle without a plate, from a camera in no lane, missing its scene photo
	p.Send(PlateRead{Camera: "5c-f2-07-5f-fd-9d", CameraName: "N Gate Entry LPR", NoPlate: true, CameraPlate: "Unknown000",
		CaptureTime: "2026-10-17-08-09-17-557", PlateFile: plateFile, Missing: "scene", PlateArrived: time.Now()})
	data, _ = os.ReadFile(filepath.Join(queue, "lpr_5c-f2-07-5f-fd-9d_2026-10-17-08-09-17-557.json"))
	ev = map[string]interface{}{}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	d, _ := ev["raw_details"].(map[string]interface{})
	if ev["gate_identifier"] != "N Gate Entry LPR" || ev["direction"] != "entry" || ev["lpr_plate"] != "" || ev["context_image_b64"] != "" ||
		d["no_plate"] != true || d["camera_plate"] != "Unknown000" || d["missing"] != "scene" {
		t.Errorf("vehicle without a plate: %v", ev)
	}
}
