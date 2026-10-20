package main

import (
	"os"
	"testing"
	"time"
)

func TestGates(t *testing.T) {
	g := NewGates([]GateConfig{
		{Name: "North", RAMAccount: "NORTH GATES", LoopIP: "192.168.42.71", LPRIP: "192.168.42.81", OverviewIP: "192.168.42.91", KeypadIP: "192.168.42.93"},
		{Name: "South", RAMAccount: "South Gates ", LoopIP: "192.168.42.72", LPRIP: "192.168.42.81"}, // camera address repeated
		{Name: "Workbench", RAMAccount: "north gates", LoopIP: "not-an-ip"},                          // account repeated
		{Name: "", RAMAccount: "IGNORED", LoopIP: "192.168.42.99"},                                   // no name
	}, []ExitLaneConfig{
		{Name: "North", LPRIP: "192.168.42.82"}, // same name as an entrance
		{Name: "South", LPRIP: "192.168.42.72"}, // the South loop's address: ignored
		{Name: "", LPRIP: "192.168.42.98"},      // no name
	})

	for account, want := range map[string]string{
		"NORTH GATES": "North", "north gates": "North", "SOUTH GATES": "South", " South Gates": "South",
		"WORKBENCH": "", "IGNORED": "", "": "",
	} {
		if got := g.ForAccount(account); got != want {
			t.Errorf("ForAccount(%q) = %q, want %q", account, got, want)
		}
	}

	for addr, want := range map[string]GateDevice{
		"192.168.42.71:50000":          {Gate: "North", Direction: "entry", Device: "loop"},
		"192.168.42.81:21":             {Gate: "North", Direction: "entry", Device: "lpr"}, // the first use keeps a repeated address
		"192.168.42.91:80":             {Gate: "North", Direction: "entry", Device: "overview"},
		"192.168.42.93:80":             {Gate: "North", Direction: "entry", Device: "keypad"},
		"192.168.42.82:80":             {Gate: "North", Direction: "exit", Device: "lpr"},
		"[::ffff:192.168.42.72]:50000": {Gate: "South", Direction: "entry", Device: "loop"},
	} {
		if got, ok := g.ForAddr(addr); !ok || got != want {
			t.Errorf("ForAddr(%s) = %+v, %v; want %+v", addr, got, ok, want)
		}
	}
	for _, addr := range []string{"192.168.42.99:1", "192.168.42.98:1", "192.168.42.62:1", "garbage"} {
		if d, ok := g.ForAddr(addr); ok {
			t.Errorf("ForAddr(%s) = %+v, want no device", addr, d)
		}
	}

	s := g.DeviceSources([]string{"10.0.0.5"})
	for addr, want := range map[string]bool{
		"192.168.42.71:1": true, "192.168.42.82:1": true, "10.0.0.5:1": true, "127.0.0.1:1": true,
		"192.168.42.99:1": false, "192.168.42.62:1": false,
	} {
		if got := s.Allowed(addr); got != want {
			t.Errorf("DeviceSources allows %s = %v, want %v", addr, got, want)
		}
	}
	if n := g.Count(); n != 3 {
		t.Errorf("Count() = %d, want 3: two entrances and the North exit (Workbench and the South exit have nothing valid)", n)
	}
}

// TestDoorKingRecordsGates checks that DoorKing records are tagged with their gate.
func TestDoorKingRecordsGates(t *testing.T) {
	data, _ := os.ReadFile("testdata/ram_records.bin")
	dir := readThroughFile(t, string(data), NewGates([]GateConfig{{Name: "Workbench", RAMAccount: "WORKBENCH"}}, nil))
	_, entries := readJournal(t, dir)
	tagged, untagged := 0, 0
	for _, e := range entries {
		if e["kind"] != "ram_record" || e["account"] == nil {
			continue
		}
		switch {
		case e["account"] == "WORKBENCH" && e["gate"] == "Workbench":
			tagged++
		case e["account"] != "WORKBENCH" && e["gate"] == nil:
			untagged++ // SOUTH GATES NEW has no gate here
		default:
			t.Errorf("record tagged wrongly: %v", e)
		}
	}
	if tagged == 0 || untagged == 0 {
		t.Errorf("tagged %d WORKBENCH records and %d others, want some of each", tagged, untagged)
	}
}

func TestOverviewCamerasLead(t *testing.T) {
	lead := 6.5
	g := NewGates(
		[]GateConfig{{Name: "South Enter", OverviewIP: "192.168.107.141"}, {Name: "North Enter", OverviewIP: "192.168.107.10", PhotoLead: &lead}},
		[]ExitLaneConfig{{Name: "North Exit", LPRIP: "192.168.107.139"}},
	)
	cams := g.OverviewCameras()
	if c := cams["entry South Enter"]; c.IP != "192.168.107.141" || c.Lead != overviewPhotoLead {
		t.Errorf("South Enter: %+v, want the default lead", c)
	}
	if c := cams["entry North Enter"]; c.IP != "192.168.107.10" || c.Lead != 6500*time.Millisecond {
		t.Errorf("North Enter: %+v, want 6.5 s", c)
	}
	if _, ok := cams["exit North Exit"]; ok || len(cams) != 2 {
		t.Errorf("cameras: %+v", cams)
	}
}
