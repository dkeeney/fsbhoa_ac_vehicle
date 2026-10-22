package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// PlateEvents sends each LPR camera read to the vehicle log on its own, with no credential,
// when the "plate_events" setting is on: entrance and exit reads, with the plate crop and a
// smaller copy of the scene photo. Like DoorKingEvents, it is a stopgap until per-lane
// correlation (CLAUDE.md, "Build order", step 3) exists, and is off by default.
type PlateEvents struct {
	enabled    atomic.Bool
	dispatcher *Dispatcher
	photos     *OverviewPhotos // the lane's overview photo of the car; nil: none
}

// PlateRead is one camera read, as journaled (lpr.go).
type PlateRead struct {
	Camera       string // MAC folder name
	CameraName   string
	CameraIP     string
	Gate         string // lane name; "" if the camera is in no lane
	Direction    string // "entry" or "exit"; "" if the camera is in no lane
	Plate        string // "" for a vehicle without a readable plate
	NoPlate      bool
	CameraPlate  string // the camera's text for a vehicle without a plate (Unknown000)
	CaptureTime  string // the file names' capture time, as written (2026-10-17-08-09-17-557)
	PlateFile    string
	SceneFile    string
	PlateArrived time.Time
	SceneArrived time.Time
	Missing      string // "plate" or "scene"
	Note         string // for the card, such as "Arm already up 2 min 56 s"; "" for none
	NotePhoto    []byte // goes with the note
}

// sceneShrink is how much the scene photo (2592 x 1520, about 1 MB) is reduced in each
// direction before it goes into the database, and the JPEG quality it is saved at: about
// 100 KB. The originals stay in the upload folder for 7 days.
const (
	sceneShrink  = 3
	sceneQuality = 80
)

func NewPlateEvents(dispatcher *Dispatcher, enabled bool) *PlateEvents {
	p := &PlateEvents{dispatcher: dispatcher}
	p.SetEnabled(enabled)
	return p
}

// SetPhotos sets where each read's overview photo comes from. Call before reads are sent.
func (p *PlateEvents) SetPhotos(o *OverviewPhotos) {
	p.photos = o
}

func (p *PlateEvents) SetEnabled(on bool) {
	if p.enabled.Swap(on) != on {
		log.Printf("[CONFIG] Plate read vehicle events: %v", on)
	}
}

// Send queues one read as a vehicle event, when the setting is on. It reports whether an
// event was queued.
func (p *PlateEvents) Send(r PlateRead) bool {
	if !p.enabled.Load() {
		return false
	}

	gate := r.Gate
	if gate == "" {
		gate = r.CameraName // a camera in no lane still shows where it came from
	}
	if gate == "" {
		gate = r.Camera
	}
	direction := r.Direction
	if direction != "exit" {
		direction = "entry"
	}
	// The capture time is when the car was there; the files arrive 1 to 7 seconds later
	when := lprCaptureTimeText(r.CaptureTime)
	if !lprEventTime.MatchString(when) {
		first := r.PlateArrived
		if first.IsZero() || (!r.SceneArrived.IsZero() && r.SceneArrived.Before(first)) {
			first = r.SceneArrived
		}
		when = first.Local().Format("2006-01-02 15:04:05.000")
	}

	details := map[string]interface{}{
		"source":       "plate_only", // not correlated with a credential
		"camera":       r.Camera,
		"camera_name":  r.CameraName,
		"camera_ip":    r.CameraIP,
		"direction":    direction,
		"capture_time": lprCaptureTimeText(r.CaptureTime),
		"source_host":  hostname,
	}
	if !r.PlateArrived.IsZero() {
		details["plate_arrived"] = r.PlateArrived.Format(time.RFC3339Nano)
	}
	if !r.SceneArrived.IsZero() {
		details["scene_arrived"] = r.SceneArrived.Format(time.RFC3339Nano)
	}
	if r.NoPlate {
		details["no_plate"], details["camera_plate"] = true, r.CameraPlate
	}
	if r.Missing != "" {
		details["missing"] = r.Missing
	}

	// The overview camera's photo of the car at the gate, taken a few seconds before the
	// capture (photo.go). Only with the camera's capture time: arrival is too late to say.
	var overview []byte
	if p.photos != nil && r.Gate != "" {
		if capture, err := time.ParseInLocation("2006-01-02 15:04:05.000", lprCaptureTimeText(r.CaptureTime), time.Local); err == nil {
			var at time.Time
			if overview, at = p.photos.ForRead(r.Gate, direction, capture); overview != nil {
				details["overview_photo_at"] = at.Format(time.RFC3339Nano)
			}
		}
	}

	// One event per read: a retried post of the same read is stored once
	uid := "lpr_" + r.Camera + "_" + r.CaptureTime
	payload, err := json.Marshal(map[string]interface{}{
		"event_uid":          uid,
		"event_timestamp":    when,
		"gate_identifier":    gate,
		"direction":          direction,
		"lpr_plate":          r.Plate,
		"lpr_image_b64":      fileB64(r.PlateFile, false),
		"context_image_b64":  fileB64(r.SceneFile, true),
		"overview_image_b64": b64(overview),
		"event_note":         r.Note,
		"note_image_b64":     b64(r.NotePhoto),
		"raw_details":        details,
	})
	if err != nil {
		log.Printf("[ERROR] Plate event JSON: %v", err)
		return false
	}
	if err := p.dispatcher.Enqueue(uid, payload); err != nil {
		log.Printf("[ERROR] Could not queue plate event %s: %v", uid, err)
		return false
	}
	log.Printf("[DISPATCH] Plate event %s: %s %s %q", uid, gate, direction, r.Plate)
	return true
}

// unreadPhotoBefore: an unread car's photo is the frame this long before it entered "beyond".
// A plate read's photo is overviewPhotoLead (4 s) before its capture, which is about 3.5 s
// after the car enters "beyond" at South Enter (2026-10-21 and 22).
const unreadPhotoBefore = 500 * time.Millisecond

// SendUnread queues a car that came in with no plate read (unread.go) as a vehicle event with
// no plate and only the overview photo, when the setting is on, so the monitor has an entry
// for it too (core's card says "NO PLATE READ" for an entry without a plate or plate photos).
func (p *PlateEvents) SendUnread(c unreadCar) bool {
	if !p.enabled.Load() {
		return false
	}
	details := map[string]interface{}{
		"source":         "count_only", // a car counted by the overview camera, not read by the LPR camera
		"no_plate_read":  true,
		"camera_ip":      c.camera,
		"direction":      "entry",
		"approach_start": c.approach.Format(time.RFC3339Nano),
		"beyond_start":   c.start.Format(time.RFC3339Nano),
		"beyond_end":     c.end.Format(time.RFC3339Nano),
		"source_host":    hostname,
	}
	var overview []byte
	if p.photos != nil {
		var at time.Time
		if overview, at = p.photos.Near(c.camera, c.start.Add(-unreadPhotoBefore)); overview != nil {
			details["overview_photo_at"] = at.Format(time.RFC3339Nano)
		}
	}
	uid := "unread_" + c.camera + "_" + c.start.Local().Format("20060102-150405.000")
	payload, err := json.Marshal(map[string]interface{}{
		"event_uid":          uid,
		"event_timestamp":    c.start.Local().Format("2006-01-02 15:04:05.000"),
		"gate_identifier":    c.gate,
		"direction":          "entry",
		"lpr_plate":          "",
		"overview_image_b64": b64(overview),
		"event_note":         c.note,
		"note_image_b64":     b64(c.notePhoto),
		"raw_details":        details,
	})
	if err != nil {
		log.Printf("[ERROR] Unread car event JSON: %v", err)
		return false
	}
	if err := p.dispatcher.Enqueue(uid, payload); err != nil {
		log.Printf("[ERROR] Could not queue unread car event %s: %v", uid, err)
		return false
	}
	log.Printf("[DISPATCH] Unread car event %s: %s", uid, c.gate)
	return true
}

// b64 returns a photo base64-encoded; "" if there is none.
func b64(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// fileB64 returns a photo base64-encoded, shrunk first if asked; "" if there is none.
func fileB64(path string, shrink bool) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[WARN] Plate event: cannot read %s: %v", path, err)
		return ""
	}
	if shrink {
		if small, err := shrinkJPEG(data, sceneShrink, sceneQuality); err == nil {
			data = small
		} else {
			log.Printf("[WARN] Plate event: cannot shrink %s (%v); sending it as it is", path, err)
		}
	}
	return base64.StdEncoding.EncodeToString(data)
}

// shrinkJPEG reduces a JPEG by factor in each direction, averaging each factor x factor block.
func shrinkJPEG(data []byte, factor, quality int) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx()/factor, b.Dy()/factor
	if w < 1 || h < 1 {
		return data, nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(factor * factor)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					cr, cg, cb, _ := src.At(b.Min.X+x*factor+dx, b.Min.Y+y*factor+dy).RGBA()
					r, g, bl = r+cr, g+cg, bl+cb
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
