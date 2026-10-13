package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileRecord returns one record as RAM's Disk File holds it: an empty line, then the record
// padded with spaces (CLAUDE.md, "Disk File destination").
func fileRecord(code string) string {
	line := fmt.Sprintf("%-23s%-12s%-9s%-13s%-8s%-18s%-8s%s", "WORKBENCH", "10/13/26", "1:23PM", "Cards", code, "ABERNATHY, BREN", "Admit", "Relay 2")
	return "\r\n" + fmt.Sprintf("%-156s", line) + "\r\n"
}

func appendTo(t *testing.T, path, data string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// fileCodes returns the codes of the journal's ram_record entries that came from the file.
func fileCodes(t *testing.T, dir string) []string {
	_, entries := readJournal(t, dir)
	var codes []string
	for _, e := range entries {
		if e["kind"] == "ram_record" && e["source"] == "file" {
			codes = append(codes, e["code"].(string))
		}
	}
	return codes
}

func TestRAMFile(t *testing.T) {
	ramFileRotateSettle = 0
	journalDir := t.TempDir()
	j, err := NewJournal(journalDir, 30)
	if err != nil {
		t.Fatal(err)
	}
	l := NewDoorKingRecords(j, func(string, ...interface{}) {})
	liveDir := filepath.Join(t.TempDir(), "Live_Testbed")
	os.MkdirAll(liveDir, 0755)
	path := filepath.Join(liveDir, "stream.txt")
	stateDir := t.TempDir()
	now := time.Now()

	// No file yet: nothing happens
	f := NewRAMFile(path, stateDir, 30, l)
	f.poll(now)

	// Two records and half of a third: only the complete ones are read
	third := fileRecord("00003")
	appendTo(t, path, fileRecord("00001")+fileRecord("00002")+third[:60])
	f.poll(now)
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00001,00002" {
		t.Fatalf("after the first read: %s", got)
	}
	appendTo(t, path, third[60:])
	f.poll(now)
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00001,00002,00003" {
		t.Fatalf("after completing the third record: %s", got)
	}

	// A restart continues from the saved position
	f = NewRAMFile(path, stateDir, 30, l)
	f.load()
	appendTo(t, path, fileRecord("00004"))
	f.poll(now)
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00001,00002,00003,00004" {
		t.Fatalf("after a restart: %s", got)
	}

	// The next day the file is rotated, after its last records are read
	appendTo(t, path, fileRecord("00005"))
	old := filepath.Join(liveDir, "stream_2000-01-01.txt")
	appendTo(t, old, fileRecord("00000"))
	tomorrow := now.AddDate(0, 0, 1)
	f.poll(tomorrow)
	rotated := filepath.Join(liveDir, "stream_"+now.Format("2006-01-02")+".txt")
	if !fileExists(rotated) || fileExists(path) {
		t.Fatalf("rotation: %s exists %v, %s exists %v", rotated, fileExists(rotated), path, fileExists(path))
	}
	if fileExists(old) {
		t.Errorf("%s is older than 30 days and wasn't removed", old)
	}
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00001,00002,00003,00004,00005" {
		t.Fatalf("after rotation: %s", got)
	}

	// RAM starts a new file: read from its start
	appendTo(t, path, fileRecord("00006"))
	f.poll(tomorrow)
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00001,00002,00003,00004,00005,00006" {
		t.Fatalf("after the new file: %s", got)
	}

	// A file replaced by someone else (shorter than the position) is read from its start
	os.Remove(path)
	appendTo(t, path, fileRecord("00007"))
	f.poll(tomorrow)
	if got := fileCodes(t, journalDir); got[len(got)-1] != "00007" {
		t.Fatalf("after the replaced file: %v", got)
	}
	if st := f.Status(); st["error"] != nil || st["last_record_at"] == nil {
		t.Errorf("status: %v", st)
	}
}

// The first start with the file as the source skips the records already in it, which came
// through the stream; a file that appears later is read from its start.
func TestRAMFileFirstStart(t *testing.T) {
	journalDir := t.TempDir()
	j, err := NewJournal(journalDir, 30)
	if err != nil {
		t.Fatal(err)
	}
	l := NewDoorKingRecords(j, func(string, ...interface{}) {})
	path := filepath.Join(t.TempDir(), "stream.txt")
	appendTo(t, path, fileRecord("00001"))
	f := NewRAMFile(path, t.TempDir(), 30, l)
	f.load()
	f.poll(time.Now())
	appendTo(t, path, fileRecord("00002"))
	f.poll(time.Now())
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00002" {
		t.Fatalf("existing file: read %q, want only 00002", got)
	}

	path2 := filepath.Join(t.TempDir(), "stream.txt")
	f2 := NewRAMFile(path2, t.TempDir(), 30, l)
	f2.load()
	f2.poll(time.Now()) // no file yet
	appendTo(t, path2, fileRecord("00003"))
	f2.poll(time.Now())
	if got := strings.Join(fileCodes(t, journalDir), ","); got != "00002,00003" {
		t.Fatalf("file appearing later: read %q, want 00002,00003", got)
	}
}

func TestRAMFileAllowed(t *testing.T) {
	cases := []struct {
		path, env string
		ok        bool
	}{
		{"/mnt/shared/Automation/DoorKing/Live_Testbed/stream.txt", "testbed", true},
		{"/mnt/shared/Automation/DoorKing/Live_Production/stream.txt", "production", true},
		{"/mnt/shared/Automation/DoorKing/Live_Production/stream.txt", "testbed", false},
		{"/mnt/shared/Automation/DoorKing/Live_Testbed/stream.txt", "", false},
		{"/mnt/shared/Automation/DoorKing/stream.txt", "testbed", false},
	}
	for _, c := range cases {
		if err := ramFileAllowed(c.path, c.env); (err == nil) != c.ok {
			t.Errorf("ramFileAllowed(%s, %q) = %v, want allowed %v", c.path, c.env, err, c.ok)
		}
	}
}

// readThroughFile writes data to a Disk File and reads it the way vehicle_service does. It
// returns the journal's folder.
func readThroughFile(t *testing.T, data string, gates *Gates) string {
	t.Helper()
	journalDir := t.TempDir()
	j, err := NewJournal(journalDir, 30)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDoorKingRecords(j, func(string, ...interface{}) {})
	if gates != nil {
		d.SetGates(gates)
	}
	path := filepath.Join(t.TempDir(), "stream.txt")
	f := NewRAMFile(path, t.TempDir(), 30, d)
	appendTo(t, path, data)
	f.poll(time.Now())
	return journalDir
}
