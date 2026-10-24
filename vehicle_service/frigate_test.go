package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFrigateEntry(t *testing.T) {
	parse := func(s string) frigateMessage {
		var m frigateMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	newObj := parse(`{"type":"new","before":{},"after":{"id":"1761160000.1-abc","camera":"south_enter","label":"car","score":0.71,"top_score":0.71,"start_time":1761160000.1,"end_time":null,"current_zones":[],"entered_zones":[]}}`)
	if e := frigateEntry(newObj); e == nil || e["event"] != "new" || e["label"] != "car" {
		t.Fatalf("new: %v", e)
	}
	same := parse(`{"type":"update","before":{"entered_zones":["approach"]},"after":{"id":"x","entered_zones":["approach"],"current_zones":["approach"]}}`)
	if e := frigateEntry(same); e != nil {
		t.Fatalf("an update with no new zone was journaled: %v", e)
	}
	zone := parse(`{"type":"update","before":{"entered_zones":["approach"]},"after":{"id":"x","entered_zones":["approach","beyond"],"current_zones":["beyond"]}}`)
	if e := frigateEntry(zone); e == nil || e["event"] != "zone" || e["zone"] != "beyond" {
		t.Fatalf("zone: %v", e)
	}
	end := parse(`{"type":"end","before":{},"after":{"id":"x","label":"truck","start_time":1761160000.1,"end_time":1761160012.6,"entered_zones":["approach","beyond"]}}`)
	if e := frigateEntry(end); e == nil || e["event"] != "end" || e["end"] == nil {
		t.Fatalf("end: %v", e)
	}
}

// Crossed finds a crossing on the lane's camera, by name, only inside the window.
func TestFrigateCrossed(t *testing.T) {
	f := NewFrigateEvents("", "", nil)
	at := time.Date(2026, 10, 23, 18, 49, 56, 0, time.Local)
	f.addCrossing("south_enter", at)
	if !f.Crossed("South Enter", at.Add(-time.Second), at.Add(time.Second)) {
		t.Fatal("missed the crossing")
	}
	if f.Crossed("North Enter", at.Add(-time.Second), at.Add(time.Second)) || f.Crossed("South Enter", at.Add(time.Second), at.Add(5*time.Second)) {
		t.Fatal("crossing found on another lane or outside the window")
	}
}

// A back-out that Frigate saw as a car going in: from the arm, a car again; at the keypad, still
// a back-out but marked (and so not sent).
func TestCountFrigateSecondOpinion(t *testing.T) {
	c := NewCarCounter(nil, t.Logf)
	c.gates.Store(NewGates([]GateConfig{{Name: "South Enter", OverviewIP: "192.0.2.41"}}, nil))
	t0 := time.Date(2026, 10, 23, 18, 49, 56, 0, time.Local)
	c.SetFrigate(func(gate string, from, to time.Time) bool {
		return gate == "South Enter" && !t0.Before(from) && !t0.After(to)
	})
	back := countEvent{kind: "car_backed", data: map[string]interface{}{"went_back": true}, approach: t0.Add(-8 * time.Second), start: t0, end: t0.Add(2 * time.Second)}
	if e := c.classify("192.0.2.41", back); e.kind != "car" || e.data["frigate_car"] != true {
		t.Fatalf("went back with a Frigate crossing: %s %v, want a car", e.kind, e.data)
	}
	keypad := countEvent{kind: "car_backed", data: map[string]interface{}{}, approach: t0.Add(-5 * time.Second), end: t0.Add(-4 * time.Second)}
	if e := c.classify("192.0.2.41", keypad); e.kind != "car_backed" || e.data["frigate_car"] != true {
		t.Fatalf("keypad back-out with a Frigate crossing: %s %v", e.kind, e.data)
	}
	alone := countEvent{kind: "car_backed", data: map[string]interface{}{"went_back": true}, approach: t0.Add(-60 * time.Second), start: t0.Add(-50 * time.Second), end: t0.Add(-48 * time.Second)}
	if e := c.classify("192.0.2.41", alone); e.kind != "car_backed" || e.data["frigate_car"] != nil {
		t.Fatalf("went back with no crossing: %s %v, want a back-out", e.kind, e.data)
	}
}
