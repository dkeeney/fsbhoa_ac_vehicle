package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeQueue struct {
	mu     sync.Mutex
	events []map[string]interface{}
}

func (q *fakeQueue) Enqueue(id string, payload []byte) error {
	var e map[string]interface{}
	if err := json.Unmarshal(payload, &e); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = append(q.events, e)
	return nil
}

func (q *fakeQueue) list() []map[string]interface{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]map[string]interface{}(nil), q.events...)
}

// The arm down without its tip long enough raises one alert, which holds while the stub goes
// up and down, and ends when the whole arm is down again. A normal rise (the tip goes off a
// second before the pivot) raises nothing.
func TestArmAlerts(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	q := &fakeQueue{}
	noSnapshot := func(string) ([]byte, error) { return nil, errors.New("no camera in the test") }
	a := NewArmAlerts(q, journal, dir)
	a.after = 100 * time.Millisecond
	a.snapshot = noSnapshot
	a.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
	ip := "192.0.2.41"
	at := time.Date(2026, 10, 20, 12, 31, 0, 0, time.Local)
	state := func(region string, on bool) {
		at = at.Add(time.Second)
		a.HandleState(ip, region, on, at)
	}

	// The whole arm down, then a normal rise and fall
	state(armDownRegion, true)
	state(armTipRegion, true)
	state(armTipRegion, false)
	state(armDownRegion, false)
	time.Sleep(200 * time.Millisecond)
	state(armDownRegion, true)
	state(armTipRegion, true)
	if n := len(q.list()); n != 0 {
		t.Fatalf("a normal cycle sent %d events", n)
	}

	// Broken: down without the tip
	state(armTipRegion, false)
	time.Sleep(200 * time.Millisecond)
	ev := q.list()
	if len(ev) != 1 || ev[0]["alert_key"] != "arm_broken" || ev[0]["alert_active"] != true || ev[0]["gate_identifier"] != "South Enter" {
		t.Fatalf("after the break: %v", ev)
	}
	// The stub goes up and down: nothing new
	state(armDownRegion, false)
	state(armDownRegion, true)
	time.Sleep(200 * time.Millisecond)
	if n := len(q.list()); n != 1 {
		t.Fatalf("the stub's cycle sent %d events, want still 1", n)
	}

	// A restart keeps the alert on
	b := NewArmAlerts(q, journal, dir)
	b.after = a.after
	b.snapshot = noSnapshot
	b.HandleState(ip, armDownRegion, true, at.Add(time.Minute))
	b.HandleState(ip, armTipRegion, true, at.Add(time.Minute))
	time.Sleep(100 * time.Millisecond) // alerts are sent in the background
	ev = q.list()
	if len(ev) != 2 || ev[1]["alert_active"] != false || ev[1]["alert_text"] != "Arm OK again" {
		t.Fatalf("the arm repaired after a restart: %v", ev)
	}
}

func TestArmHeld(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	q := &fakeQueue{}
	a := NewArmAlerts(q, journal, dir)
	a.heldAfter = 100 * time.Millisecond
	a.snapshot = func(string) ([]byte, error) { return nil, errors.New("no camera in the test") }
	a.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
	ip := "192.0.2.41"
	at := time.Date(2026, 10, 22, 18, 2, 0, 0, time.Local)

	// A normal cycle: up for less than heldAfter
	a.HandleState(ip, armDownRegion, true, at)
	a.HandleState(ip, armDownRegion, false, at.Add(time.Second))
	time.Sleep(50 * time.Millisecond)
	a.HandleState(ip, armDownRegion, true, at.Add(2*time.Second))
	time.Sleep(150 * time.Millisecond)
	if n := len(q.list()); n != 0 {
		t.Fatalf("a normal cycle sent %d events", n)
	}

	// Up with no car through (rule 3): one notice, not an alarm, timed from when it went up
	up := at.Add(time.Minute)
	a.HandleState(ip, armDownRegion, false, up)
	time.Sleep(200 * time.Millisecond)
	ev := q.list()
	if len(ev) != 1 || ev[0]["alert_key"] != "arm_held" || ev[0]["alert_active"] != true || ev[0]["alert_notice"] != true ||
		ev[0]["alert_text"] != "Arm up, no cars through" || ev[0]["event_timestamp"] != "2026-10-22 18:03:00.000" {
		t.Fatalf("held up: %v", ev)
	}

	// A restart keeps it noted; the arm coming down ends it, saying for how long
	b := NewArmAlerts(q, journal, dir)
	b.heldAfter = a.heldAfter
	b.snapshot = a.snapshot
	b.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
	b.HandleState(ip, armDownRegion, false, up.Add(2*time.Hour))
	time.Sleep(200 * time.Millisecond)
	if n := len(q.list()); n != 1 {
		t.Fatalf("still up after a restart sent %d events, want still 1", n)
	}
	b.HandleState(ip, armDownRegion, true, up.Add(2*time.Hour+14*time.Minute))
	time.Sleep(100 * time.Millisecond)
	ev = q.list()
	if len(ev) != 2 || ev[1]["alert_key"] != "arm_held" || ev[1]["alert_active"] != false || ev[1]["alert_notice"] != true ||
		ev[1]["alert_text"] != "Arm down again (up 2 h 14 min)" {
		t.Fatalf("down again: %v", ev)
	}
}

func TestArmCarCrossed(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	q := &fakeQueue{}
	a := NewArmAlerts(q, journal, dir)
	a.heldCar = 100 * time.Millisecond
	snapped := make(chan string, 4)
	a.snapshot = func(ip string) ([]byte, error) { snapped <- ip; return []byte("keypad photo"), nil }
	a.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41", KeypadIP: "192.0.2.34"}}, nil))
	ip := "192.0.2.41"
	at := time.Now()

	// An ordinary cycle: the car that opened the arm goes through 8 s later, then it comes down
	a.HandleState(ip, armDownRegion, true, at)
	a.HandleState(ip, armDownRegion, false, at.Add(time.Second))
	if got := <-snapped; got != "192.0.2.34" {
		t.Fatalf("the opener's photo came from %s, want the keypad camera", got)
	}
	time.Sleep(20 * time.Millisecond)
	if note, _ := a.CarCrossed("South Enter", at.Add(9*time.Second)); note != "" {
		t.Fatalf("an ordinary car got a note: %q", note)
	}
	a.HandleState(ip, armDownRegion, true, at.Add(12*time.Second))
	time.Sleep(200 * time.Millisecond)
	if n := len(q.list()); n != 0 {
		t.Fatalf("an ordinary cycle sent %d events", n)
	}

	// Rule 1: a cyclist raises the arm; the next car, 2 min 56 s later, gets the note and the
	// keypad photo, and a second car right behind it doesn't
	up := at.Add(time.Minute)
	a.HandleState(ip, armDownRegion, false, up)
	<-snapped
	time.Sleep(20 * time.Millisecond)
	note, photo := a.CarCrossed("South Enter", up.Add(2*time.Minute+56*time.Second))
	if note != "Arm already up 2 min 56 s" || string(photo) != "keypad photo" {
		t.Fatalf("the car under a raised arm: %q %q", note, photo)
	}
	if note, _ := a.CarCrossed("South Enter", up.Add(3*time.Minute+5*time.Second)); note != "" {
		t.Fatalf("the second car got a note: %q", note)
	}
	if note, _ := a.CarCrossed("North Enter", up.Add(3*time.Minute)); note != "" {
		t.Fatalf("another lane's car got a note: %q", note)
	}

	// Rule 2: the arm still up heldCar after a car went through: held
	time.Sleep(200 * time.Millisecond)
	ev := q.list()
	if len(ev) != 1 || ev[0]["alert_text"] != "Arm held up" || ev[0]["alert_notice"] != true || ev[0]["event_timestamp"] != up.Local().Format("2006-01-02 15:04:05.000") {
		t.Fatalf("held after a car: %v", ev)
	}
	a.HandleState(ip, armDownRegion, true, up.Add(2*time.Hour+14*time.Minute))
	time.Sleep(100 * time.Millisecond)
	if ev = q.list(); len(ev) != 2 || ev[1]["alert_text"] != "Arm down again (up 2 h 14 min)" {
		t.Fatalf("down again: %v", ev)
	}
}

// With an "arm-up" region: a white truck body in the lowered arm's box doesn't bring the arm
// down while the raised arm is seen, the arm moving (neither region) keeps its position, and a
// rise is dated from when "arm-down" stopped seeing it.
func TestArmUpRegion(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	a := NewArmAlerts(&fakeQueue{}, journal, dir)
	a.snapshot = func(string) ([]byte, error) { return nil, errors.New("no camera in the test") }
	a.SetGates(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
	ip, lane := "192.0.2.41", "South Enter"
	at := time.Date(2026, 10, 22, 12, 29, 0, 0, time.Local)
	sec := func(n int) time.Time { return at.Add(time.Duration(n) * time.Second) }

	a.HandleState(ip, armDownRegion, true, sec(0))
	a.HandleState(ip, armUpRegion, false, sec(0))
	a.HandleState(ip, armDownRegion, false, sec(10)) // starts rising
	if a.ArmUp(lane, sec(11)) {
		t.Fatal("up while rising, before the raised arm is seen")
	}
	a.HandleState(ip, armUpRegion, true, sec(12)) // fully up
	if !a.ArmUp(lane, sec(10)) || a.ArmUp(lane, sec(9)) {
		t.Fatal("the rise isn't dated from when the lowered arm's box emptied")
	}
	a.HandleState(ip, armDownRegion, true, sec(20)) // a white truck body in the lowered arm's box
	if !a.ArmUp(lane, sec(21)) {
		t.Fatal("a truck body brought the arm down while the raised arm was seen")
	}
	a.HandleState(ip, armDownRegion, false, sec(25))
	a.HandleState(ip, armUpRegion, false, sec(40)) // coming down
	if !a.ArmUp(lane, sec(41)) {
		t.Fatal("down while still coming down")
	}
	a.HandleState(ip, armDownRegion, true, sec(42)) // down
	if a.ArmUp(lane, sec(43)) || !a.ArmUp(lane, sec(41)) {
		t.Fatal("not down once the lowered arm is seen, or the last up period lost")
	}
}
