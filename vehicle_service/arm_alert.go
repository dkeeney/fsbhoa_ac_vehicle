package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ArmAlerts tells the staff when a lane's arm is broken. A lane's overview camera has two
// "bar" regions on the lowered arm (motion.go): "arm-down" near its pivot and "arm-tip" on
// its outer part. The arm down at its pivot without its tip, for the lane's wait (the Entrance
// Lanes table's "Arm alert after"; blank: armBrokenAfter), is a broken
// arm (2026-10-20 12:31: a truck towing a wood chipper broke South Enter's arm in half). The
// alert goes to the vehicle log as a lane alert (core's alert_key "arm_broken"), which the
// live monitor shows as a red banner with a sound, until the whole arm is seen down again.
//
// The alert holds while the stub goes up and down, and across a restart (the state file).
//
// The same "arm-down" region also tells when the arm has been up for armHeldAfter: staff hold
// it up for long periods (Bingo night, with a schedule in RAM), and an arm stuck up or missing
// looks the same. That is a notice, not an alarm (core's alert_notice): "Arm held up" when
// it starts, "Arm down again" when the arm comes down (TODO #32, "The arm held up").
type ArmAlerts struct {
	mu         sync.Mutex
	arms       map[string]*armState // by camera IP
	after      time.Duration
	dispatcher enqueuer
	journal    *Journal
	photos     *OverviewPhotos // the photo sent with the alert; nil: none
	gates      atomic.Pointer[Gates]
	stateFile  string
	heldFile   string
	heldAfter  time.Duration
	snapshot   func(ip string) ([]byte, error) // the camera's own snapshot, when no frame is kept
}

const (
	armDownRegion   = "arm-down"
	armTipRegion    = "arm-tip"
	armAlertKey     = "arm_broken"
	armBrokenAfter  = 3 * time.Minute // the tip goes off about a second before the pivot each time the arm rises, and haze or a car can hide it for a minute or so (North Enter's .11, 2026-10-21)
	armAlertStateFn = "arm_alerts.json"
	armHeldKey      = "arm_held"
	// An ordinary cycle is up 5 to 30 s; a driver who stops under the raised arm keeps it up
	// longer (2026-10-20 and 21 at South Enter: three times about 3 minutes, one car each).
	armHeldAfter   = 5 * time.Minute
	armHeldStateFn = "arm_held.json"
	armHeldStale   = 30 * time.Second // the camera's newest frame is older than this: it isn't watching the arm
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
	upSince             time.Time   // the arm was last seen going up; zero: it is down (or not known)
	heldTimer           *time.Timer // gives the held-up notice once the arm has been up armHeldAfter
	heldSince           time.Time   // the held-up notice is on, since; zero: off
}

func NewArmAlerts(dispatcher enqueuer, journal *Journal, stateDir string) *ArmAlerts {
	a := &ArmAlerts{
		arms:       map[string]*armState{},
		after:      armBrokenAfter,
		dispatcher: dispatcher,
		journal:    journal,
		stateFile:  filepath.Join(stateDir, armAlertStateFn),
		heldFile:   filepath.Join(stateDir, armHeldStateFn),
		heldAfter:  armHeldAfter,
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
		a.armHeld(ip, s, on, at)
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
			after := a.after
			if g := a.gates.Load(); g != nil {
				if d, ok := g.ArmAlertAfter(ip); ok {
					after = d
				}
			}
			s.timer = time.AfterFunc(after, func() { a.confirm(ip, since) })
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

// armHeld follows the arm going up and coming down, for the held-up notice (called with a.mu held).
func (a *ArmAlerts) armHeld(ip string, s *armState, down bool, at time.Time) {
	if !down {
		if s.upSince.IsZero() && s.heldSince.IsZero() {
			s.upSince = at
			since := at
			s.heldTimer = time.AfterFunc(a.heldAfter, func() { a.confirmHeld(ip, since) })
		}
		return
	}
	s.upSince = time.Time{}
	if s.heldTimer != nil {
		s.heldTimer.Stop()
		s.heldTimer = nil
	}
	if !s.heldSince.IsZero() {
		text := "Arm down again (up " + heldFor(at.Sub(s.heldSince)) + ")"
		s.heldSince = time.Time{}
		a.saveHeld()
		go a.sendAlert(ip, armHeldKey, "arm_down_again", text, false, true, at)
	}
}

// confirmHeld gives the held-up notice when the arm has stayed up since `since`.
func (a *ArmAlerts) confirmHeld(ip string, since time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.arms[ip]
	if s == nil || !s.upSince.Equal(since) || !s.heldSince.IsZero() {
		return
	}
	// A camera that stopped sending frames says nothing new about the arm: wait for it
	if f, watched := a.photos.Latest(ip); watched && time.Since(f.at) > armHeldStale {
		s.heldTimer = time.AfterFunc(armHeldStale, func() { a.confirmHeld(ip, since) })
		return
	}
	s.heldTimer = nil
	s.heldSince = since
	a.saveHeld()
	go a.sendAlert(ip, armHeldKey, "arm_held", "Arm held up", true, true, since)
}

// heldFor says how long the arm was up: "7 min", "2 h 14 min".
func heldFor(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(m/60) + " h " + strconv.Itoa(m%60) + " min"
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

// send journals the broken arm alert's start or end and queues it for the vehicle log.
func (a *ArmAlerts) send(ip string, active bool, at time.Time) {
	kind, text := "arm_broken", "Arm broken"
	if !active {
		kind, text = "arm_ok", "Arm OK again"
	}
	a.sendAlert(ip, armAlertKey, kind, text, active, false, at)
}

// sendAlert journals a lane alert or notice (as `kind`) and queues it for the vehicle log.
func (a *ArmAlerts) sendAlert(ip, key, kind, text string, active, notice bool, at time.Time) {
	gate, direction := ip, "entry"
	if g := a.gates.Load(); g != nil {
		if d, ok := g.ForIP(ip); ok {
			gate, direction = d.Gate, d.Direction
		}
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
		"alert_key":          key,
		"alert_text":         text,
		"alert_active":       active,
		"alert_notice":       notice,
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
	writeArmState(a.stateFile, on)
}

// saveHeld writes the cameras whose held-up notice is on (called with a.mu held).
func (a *ArmAlerts) saveHeld() {
	on := map[string]time.Time{}
	for ip, s := range a.arms {
		if !s.heldSince.IsZero() {
			on[ip] = s.heldSince
		}
	}
	writeArmState(a.heldFile, on)
}

func writeArmState(file string, on map[string]time.Time) {
	data, _ := json.Marshal(on)
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[ERROR] Arm alert state: %v", err)
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		log.Printf("[ERROR] Arm alert state: %v", err)
	}
}

// load reads the alerts and held-up notices that were on when the service stopped: an alert
// stays on until the whole arm is seen down, a notice until the arm comes down.
func (a *ArmAlerts) load() {
	for ip, since := range readArmState(a.stateFile) {
		a.arms[ip] = &armState{raisedSince: since}
		log.Printf("[ARM] %s: the broken arm alert since %s is still on", ip, since.Local().Format("2006-01-02 15:04:05"))
	}
	for ip, since := range readArmState(a.heldFile) {
		if a.arms[ip] == nil {
			a.arms[ip] = &armState{}
		}
		a.arms[ip].heldSince = since
		log.Printf("[ARM] %s: the arm held up since %s is still noted", ip, since.Local().Format("2006-01-02 15:04:05"))
	}
}

func readArmState(file string) map[string]time.Time {
	on := map[string]time.Time{}
	data, err := os.ReadFile(file)
	if err != nil {
		return on
	}
	if err := json.Unmarshal(data, &on); err != nil {
		log.Printf("[ERROR] Arm alert state %s: %v", file, err)
	}
	return on
}
