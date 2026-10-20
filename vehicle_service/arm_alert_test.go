package main

import (
	"encoding/json"
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
	a := NewArmAlerts(q, journal, dir)
	a.after = 100 * time.Millisecond
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
	b.HandleState(ip, armDownRegion, true, at.Add(time.Minute))
	b.HandleState(ip, armTipRegion, true, at.Add(time.Minute))
	ev = q.list()
	if len(ev) != 2 || ev[1]["alert_active"] != false || ev[1]["alert_text"] != "Arm OK again" {
		t.Fatalf("the arm repaired after a restart: %v", ev)
	}
}
