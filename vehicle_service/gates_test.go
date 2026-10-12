package main

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestGates(t *testing.T) {
	g := NewGates([]GateConfig{
		{Name: "North", RAMAccount: "NORTH GATES", LoopIP: "192.168.42.71", LPRIP: "192.168.42.81", OverviewIP: "192.168.42.91"},
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

// TestRAMListenerGates checks that DoorKing records are tagged with their gate.
func TestRAMListenerGates(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	l := NewRAMListener(0, NewSources(nil), j, func(string, ...interface{}) {})
	l.SetGates(NewGates([]GateConfig{{Name: "Workbench", RAMAccount: "WORKBENCH"}}, nil))
	go l.serve(ln)

	data, _ := os.ReadFile("testdata/ram_records.bin")
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.Write(data)
	conn.Close()

	var entries []map[string]interface{}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var kinds map[string]int
		if kinds, entries = readJournal(t, dir); kinds["ram_disconnect"] == 1 {
			break
		}
	}
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
