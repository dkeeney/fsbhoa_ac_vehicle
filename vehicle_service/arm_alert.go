package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ArmAlerts tells the staff when a lane's arm is broken. A lane's overview camera has two
// "bar" regions on the lowered arm (motion.go): "arm-down" near its pivot and "arm-tip" on
// its outer part. The arm down at its pivot without its tip, for armBrokenAfter, is a broken
// arm (2026-10-20 12:31: a truck towing a wood chipper broke South Enter's arm in half). The
// alert goes to the vehicle log as a lane alert (core's alert_key "arm_broken"), which the
// live monitor shows as a red banner with a sound, until the whole arm is seen down again.
//
// The alert holds while the stub goes up and down, and across a restart (the state file).
type ArmAlerts struct {
	mu         sync.Mutex
	arms       map[string]*armState // by camera IP
	after      time.Duration
	dispatcher enqueuer
	journal    *Journal
	photos     *OverviewPhotos // the photo sent with the alert; nil: none
	gates      atomic.Pointer[Gates]
	stateFile  string
	snapshot   func(ip string) ([]byte, error) // the camera's own snapshot, when no frame is kept
}

const (
	armDownRegion   = "arm-down"
	armTipRegion    = "arm-tip"
	armAlertKey     = "arm_broken"
	armBrokenAfter  = 30 * time.Second // the tip goes off about a second before the pivot each time the arm rises
	armAlertStateFn = "arm_alerts.json"
)

// enqueuer queues an event for the vehicle log (the Dispatcher).
type enqueuer interface {
	Enqueue(eventID string, payload []byte) error
}

type armState struct {
	down, tip           bool
	downKnown, tipKnown bool
	brokenSince         time.Time   // when the arm was last seen down without its tip; zero: it isn't
	timer               *time.Timer // raises the alert once it has lasted armBrokenAfter
	raisedSince         time.Time   // the alert is on, since; zero: off
}

func NewArmAlerts(dispatcher enqueuer, journal *Journal, stateDir string) *ArmAlerts {
	a := &ArmAlerts{
		arms:       map[string]*armState{},
		after:      armBrokenAfter,
		dispatcher: dispatcher,
		journal:    journal,
		stateFile:  filepath.Join(stateDir, armAlertStateFn),
		snapshot: func(ip string) ([]byte, error) {
			return fetchSnapshot(&http.Client{Timeout: snapshotTimeout}, "http://"+ip+"/snapshot.JPG")
		},
	}
	a.load()
	return a
}

// SetPhotos sets where the alert's photo comes from. Call before states arrive.
func (a *ArmAlerts) SetPhotos(o *OverviewPhotos) { a.photos = o }

// SetGates gives the lanes, so the alert names the camera's lane.
func (a *ArmAlerts) SetGates(g *Gates) { a.gates.Store(g) }

// HandleState takes one region's settled state from a camera; other regions are ignored.
func (a *ArmAlerts) HandleState(ip, region string, on bool, at time.Time) {
	if region != armDownRegion && region != armTipRegion {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.arms[ip]
	if s == nil {
		s = &armState{}
		a.arms[ip] = s
	}
	if region == armDownRegion {
		s.down, s.downKnown = on, true
	} else {
		s.tip, s.tipKnown = on, true
	}
	if !s.downKnown || !s.tipKnown {
		return
	}

	if s.down && !s.tip {
		if s.brokenSince.IsZero() {
			s.brokenSince = at
			since := at
			s.timer = time.AfterFunc(a.after, func() { a.confirm(ip, since) })
		}
		return
	}
	s.brokenSince = time.Time{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	// The whole arm down again: the alert ends. An arm that is up (or gone) says nothing.
	if s.down && s.tip && !s.raisedSince.IsZero() {
		s.raisedSince = time.Time{}
		a.save()
		go a.send(ip, false, at) // not under the lock: it may fetch a snapshot
	}
}

// confirm raises the alert when the arm has stayed down without its tip since `since`.
func (a *ArmAlerts) confirm(ip string, since time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.arms[ip]
	if s == nil || !s.brokenSince.Equal(since) || !s.raisedSince.IsZero() {
		return
	}
	s.timer = nil
	s.raisedSince = since
	a.save()
	go a.send(ip, true, since)
}

// send journals the alert's start or end and queues it for the vehicle log.
func (a *ArmAlerts) send(ip string, active bool, at time.Time) {
	gate, direction := ip, "entry"
	if g := a.gates.Load(); g != nil {
		if d, ok := g.ForIP(ip); ok {
			gate, direction = d.Gate, d.Direction
		}
	}
	kind, text := "arm_broken", "Arm broken"
	if !active {
		kind, text = "arm_ok", "Arm OK again"
	}
	a.journal.Write(kind, map[string]interface{}{"camera_ip": ip, "gate": gate, "at": at.Local().Format(journalTimeFormat)})
	log.Printf("[ARM] %s (%s): %s since %s", gate, ip, text, at.Local().Format("15:04:05"))

	// The newest kept frame (a lane's overview camera), or else the camera's own snapshot (an
	// arm camera that is only watched by motion.go, such as North Enter's .11: a Speco, no login)
	var photo []byte
	if a.photos != nil {
		if f, ok := a.photos.Latest(ip); ok {
			photo = f.data
		}
	}
	if photo == nil {
		if data, err := a.snapshot(ip); err == nil {
			photo = data
			if half, err := halfJPEG(data, frameQuality); err == nil { // 1920 x 1080 to 960 x 540, as the saved frames
				photo = half
			}
		} else {
			log.Printf("[ARM] %s: no photo for the alert: %v", ip, err)
		}
	}
	uid := kind + "_" + ip + "_" + at.Local().Format("20060102-150405.000")
	payload, err := json.Marshal(map[string]interface{}{
		"event_uid":          uid,
		"event_timestamp":    at.Local().Format("2006-01-02 15:04:05.000"),
		"gate_identifier":    gate,
		"direction":          direction,
		"overview_image_b64": b64(photo),
		"alert_key":          armAlertKey,
		"alert_text":         text,
		"alert_active":       active,
		"raw_details": map[string]interface{}{
			"source":      "arm_watch",
			"camera_ip":   ip,
			"source_host": hostname,
		},
	})
	if err != nil {
		log.Printf("[ERROR] Arm alert JSON: %v", err)
		return
	}
	if err := a.dispatcher.Enqueue(uid, payload); err != nil {
		log.Printf("[ERROR] Could not queue arm alert %s: %v", uid, err)
	}
}

// save writes the cameras whose alert is on (called with a.mu held).
func (a *ArmAlerts) save() {
	on := map[string]time.Time{}
	for ip, s := range a.arms {
		if !s.raisedSince.IsZero() {
			on[ip] = s.raisedSince
		}
	}
	data, _ := json.Marshal(on)
	tmp := a.stateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[ERROR] Arm alert state: %v", err)
		return
	}
	if err := os.Rename(tmp, a.stateFile); err != nil {
		log.Printf("[ERROR] Arm alert state: %v", err)
	}
}

// load reads the alerts that were on when the service stopped: they stay on until the whole
// arm is seen down.
func (a *ArmAlerts) load() {
	data, err := os.ReadFile(a.stateFile)
	if err != nil {
		return
	}
	on := map[string]time.Time{}
	if err := json.Unmarshal(data, &on); err != nil {
		log.Printf("[ERROR] Arm alert state %s: %v", a.stateFile, err)
		return
	}
	for ip, since := range on {
		a.arms[ip] = &armState{raisedSince: since}
		log.Printf("[ARM] %s: the broken arm alert since %s is still on", ip, since.Local().Format("2006-01-02 15:04:05"))
	}
}
