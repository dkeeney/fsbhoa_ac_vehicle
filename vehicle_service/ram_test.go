package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testdata/ram_records.bin holds one record of each kind captured from the workbench RAM on
// 2026-10-09 and 10, in wire format (CR LF before and after each), with real names replaced.

func loadRecords(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("testdata/ram_records.bin")
	if err != nil {
		t.Fatal(err)
	}
	var recs []string
	for _, r := range strings.Split(string(data), "\r\n") {
		if strings.TrimSpace(r) != "" {
			recs = append(recs, r)
		}
	}
	return recs
}

func findRecord(t *testing.T, recs []string, contains string) RAMRecord {
	t.Helper()
	for _, r := range recs {
		if strings.Contains(r, contains) {
			return ParseRAMRecord(r)
		}
	}
	t.Fatalf("no test record contains %q", contains)
	return RAMRecord{}
}

func TestParseRAMRecord(t *testing.T) {
	recs := loadRecords(t)

	card := findRecord(t, recs, "06549")
	want := RAMRecord{Kind: "credential", Account: "WORKBENCH", Date: "10/09/26", Time: "4:22PM",
		Type: "Cards", Code: "06549", Name: "& SHUT, OPEN", Result: "Admit", Relay: "Relay 2"}
	want.Raw = card.Raw
	if card != want {
		t.Errorf("card record:\n got %+v\nwant %+v", card, want)
	}

	entry := findRecord(t, recs, "Entry Code")
	if entry.Kind != "credential" || entry.Type != "Entry Code" || entry.Code != "6020" || entry.Result != "Admit" || entry.Relay != "Relay 1" {
		t.Errorf("entry code record: %+v", entry)
	}

	status := findRecord(t, recs, "Database is Up To Date")
	if status.Kind != "status" || status.Type != "RS232" || status.Message != "Database is Up To Date" || status.Code != "" {
		t.Errorf("status record: %+v", status)
	}

	failed := findRecord(t, recs, "Failed To Connect")
	if failed.Kind != "status" || failed.Account != "SOUTH GATES NEW" || failed.Message != "Failed To Connect" {
		t.Errorf("network status record: %+v", failed)
	}

	// The flood record: account, time and relay only
	var flood RAMRecord
	for _, r := range recs {
		if rec := ParseRAMRecord(r); rec.Type == "" {
			flood = rec
		}
	}
	if flood.Kind != "incomplete" || flood.Relay != "Relay 1" || flood.Code != "" || flood.Result != "" {
		t.Errorf("flood record: %+v", flood)
	}

	// Short or garbled lines must not panic
	for _, s := range []string{"", "WORKBENCH", "WORKBENCH              10/10/26    7:52AM   Entry Code   60"} {
		ParseRAMRecord(s)
	}
}

func TestRepeatFilter(t *testing.T) {
	recs := loadRecords(t)
	card := findRecord(t, recs, "06549").Raw
	var flood string
	for _, r := range recs {
		if ParseRAMRecord(r).Kind == "incomplete" {
			flood = r
		}
	}
	t0 := time.Date(2026, 10, 10, 6, 23, 33, 0, time.Local)

	// The flood: bursts every ~0.3 s, all collapsed, whatever the gap
	var f repeatFilter
	writes := 0
	for i := 0; i < 100; i++ {
		if w, s := f.Add(flood, true, t0.Add(time.Duration(i)*300*time.Millisecond)); w {
			writes++
		} else if s != nil {
			t.Fatal("summary returned for a collapsed copy")
		}
	}
	if writes != 1 {
		t.Errorf("flood: %d records written, want 1", writes)
	}
	// The next different record flushes a summary of the 99 collapsed copies
	w, s := f.Add(card, false, t0.Add(31*time.Second))
	if !w || s == nil || s.count != 99 || s.raw != flood {
		t.Errorf("after flood: write=%v summary=%+v", w, s)
	}

	// Real repeat reads (about a second apart, as at the gate) are all kept
	f = repeatFilter{}
	for i, gap := range []time.Duration{0, 1004, 1064, 807, 1250, 2505} {
		t0 = t0.Add(gap * time.Millisecond)
		if w, _ := f.Add(card, false, t0); !w {
			t.Errorf("read %d (%d ms after the last) was collapsed", i, gap)
		}
	}

	// ...but an identical complete record within the window is collapsed
	if w, _ := f.Add(card, false, t0.Add(100*time.Millisecond)); w {
		t.Error("identical record 100 ms later was written")
	}
	if s := f.Flush(); s == nil || s.count != 1 {
		t.Errorf("flush: %+v", s)
	}
}

func TestSources(t *testing.T) {
	s := NewSources([]string{"192.168.70.3", " 10.0.0.5 ", "not-an-ip", ""})
	for addr, want := range map[string]bool{
		"192.168.70.3:53399":         true,
		"10.0.0.5:1":                 true,
		"127.0.0.1:5000":             true, // this machine
		"[::1]:5000":                 true,
		"[::ffff:192.168.70.3]:4000": true, // IPv4 seen through an IPv6 socket
		"192.168.70.4:53399":         false,
		"garbage":                    false,
	} {
		if got := s.Allowed(addr); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", addr, got, want)
		}
	}
}

// TestRAMListener streams the captured records, split at awkward points and with a flood,
// and checks what reaches the journal.
func TestRAMListener(t *testing.T) {
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
	l := &RAMListener{sources: NewSources(nil), journal: j, debugf: func(string, ...interface{}) {}}
	go l.serve(ln)

	data, _ := os.ReadFile("testdata/ram_records.bin")
	var flood string
	for _, r := range loadRecords(t) {
		if ParseRAMRecord(r).Kind == "incomplete" {
			flood = "\r\n" + r + "\r\n"
		}
	}
	stream := string(data) + strings.Repeat(flood, 50)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	for len(stream) > 0 { // odd chunk sizes, so records are split across reads
		n := 97
		if n > len(stream) {
			n = len(stream)
		}
		conn.Write([]byte(stream[:n]))
		stream = stream[n:]
	}
	conn.Close()

	// Wait for the disconnect entry
	var kinds map[string]int
	var records []map[string]interface{}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		kinds, records = readJournal(t, dir)
		if kinds["ram_disconnect"] == 1 {
			break
		}
	}
	if kinds["ram_connect"] != 1 || kinds["ram_disconnect"] != 1 {
		t.Fatalf("connect/disconnect entries: %v", kinds)
	}
	// The 9 test records, then 50 flood copies: the flood record is in the middle of the test
	// file, so the first trailing copy follows a different record and is written; the other
	// 49 collapse into one ram_repeat entry.
	if kinds["ram_record"] != 10 {
		t.Errorf("ram_record entries = %d, want 10 (9 distinct + the flood again after other records)", kinds["ram_record"])
	}
	if kinds["ram_repeat"] != 1 {
		t.Errorf("ram_repeat entries = %d, want 1", kinds["ram_repeat"])
	}
	for _, r := range records {
		if r["kind"] == "ram_repeat" && r["count"].(float64) != 49 {
			t.Errorf("repeat count = %v, want 49", r["count"])
		}
		if r["kind"] == "ram_record" && r["code"] == "6020" && (r["type"] != "Entry Code" || r["remote"] != "127.0.0.1") {
			t.Errorf("entry code entry: %v", r)
		}
	}
}

func readJournal(t *testing.T, dir string) (map[string]int, []map[string]interface{}) {
	files, _ := filepath.Glob(filepath.Join(dir, "journal-*.jsonl"))
	kinds := map[string]int{}
	var entries []map[string]interface{}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var e map[string]interface{}
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Fatalf("bad journal line %q: %v", sc.Text(), err)
			}
			kinds[e["kind"].(string)]++
			entries = append(entries, e)
		}
		fh.Close()
	}
	return kinds, entries
}
