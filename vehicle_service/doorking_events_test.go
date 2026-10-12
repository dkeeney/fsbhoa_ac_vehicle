package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDoorKingEvents(t *testing.T) {
	dir := t.TempDir()
	disp, err := NewDispatcher(&ServiceConfig{QueueDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDoorKingEvents(disp, false)

	recs := loadRecords(t)
	card := findRecord(t, recs, "06549")
	entry := findRecord(t, recs, "Entry Code")
	status := findRecord(t, recs, "Database is Up To Date")
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)

	if d.Handle(card, "Workbench", t0) {
		t.Fatal("queued an event while off")
	}
	d.SetEnabled(true)

	steps := []struct {
		rec  RAMRecord
		lane string
		at   time.Duration
		want bool
	}{
		{card, "Workbench", 0, true},                   // a new presentation
		{card, "Workbench", 1 * time.Second, false},    // the tag re-read a second later
		{card, "Workbench", 9 * time.Second, false},    // still sitting there
		{entry, "Workbench", 10 * time.Second, true},   // a different code
		{card, "Workbench", 25 * time.Second, true},    // 16 s after the last read: a new presentation
		{card, "", 26 * time.Second, true},             // another lane (no lane: its RAM account)
		{status, "Workbench", 27 * time.Second, false}, // a status record
	}
	for i, s := range steps {
		if got := d.Handle(s.rec, s.lane, t0.Add(s.at)); got != s.want {
			t.Errorf("step %d (%s at +%s): queued = %v, want %v", i, s.rec.Code, s.at, got, s.want)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 4 {
		t.Fatalf("%d events queued, want 4", len(files))
	}
	var first map[string]interface{}
	data, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatal(err)
	}
	if first["gate_identifier"] != "Workbench" || first["auth_id"] != "06549" || first["auth_type"] != "DK_WINDSHIELD" ||
		first["event_timestamp"] != "2026-10-12 09:00:00.000" {
		t.Errorf("first event: %v", first)
	}
	if details, _ := first["raw_details"].(map[string]interface{}); details["source"] != "doorking_only" || details["relay"] != "Relay 2" {
		t.Errorf("first event's raw_details: %v", first["raw_details"])
	}
}
