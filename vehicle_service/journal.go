package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Journal records every raw input (DoorKing records, loop changes, plates) with its arrival
// time, one JSON object per line, in a file per local day. It is the traffic-study record
// (CLAUDE.md, "Build order", step 0): nothing in it is correlated, and recordings can later
// be replayed through the correlation code as tests.
type Journal struct {
	mu       sync.Mutex
	dir      string
	keepDays int
	day      string
	file     *os.File
	lastSeen map[string]time.Time // newest entry of each kind, for the health report
}

const journalTimeFormat = "2006-01-02T15:04:05.000-07:00"

func NewJournal(dir string, keepDays int) (*Journal, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("creating journal dir %s: %w", dir, err)
	}
	j := &Journal{dir: dir, keepDays: keepDays, lastSeen: map[string]time.Time{}}
	j.purge()
	return j, nil
}

// Write appends one entry. kind names the input ("ram_record", "loop", "plate", ...);
// fields are added as they are. The arrival time is stamped here.
func (j *Journal) Write(kind string, fields map[string]interface{}) {
	j.WriteAt(time.Now(), kind, fields)
}

// WriteAt is Write with an explicit arrival time, for entries that summarise earlier input.
func (j *Journal) WriteAt(t time.Time, kind string, fields map[string]interface{}) {
	entry := make(map[string]interface{}, len(fields)+2)
	for k, v := range fields {
		entry[k] = v
	}
	entry["t"] = t.Local().Format(journalTimeFormat)
	entry["kind"] = kind
	line, err := json.Marshal(entry)
	if err != nil {
		log.Printf("[ERROR] Journal entry not written (%v): %v", err, entry)
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if t.After(j.lastSeen[kind]) {
		j.lastSeen[kind] = t
	}
	if err := j.openFor(t); err != nil {
		log.Printf("[ERROR] Journal: %v", err)
		return
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		log.Printf("[ERROR] Writing journal %s: %v", j.file.Name(), err)
	}
}

// openFor makes sure the current file is the one for t's local day. Called with mu held.
func (j *Journal) openFor(t time.Time) error {
	day := t.Local().Format("2006-01-02")
	if j.file != nil && day == j.day {
		return nil
	}
	if j.file != nil {
		j.file.Close()
		j.file = nil
		// A new day: also a good moment to drop old files
		go j.purge()
	}
	path := filepath.Join(j.dir, "journal-"+day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	j.file, j.day = f, day
	return nil
}

// purge deletes journal files older than keepDays.
func (j *Journal) purge() {
	files, err := filepath.Glob(filepath.Join(j.dir, "journal-*.jsonl"))
	if err != nil {
		return
	}
	sort.Strings(files)
	cutoff := time.Now().AddDate(0, 0, -j.keepDays).Format("2006-01-02")
	for _, f := range files {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "journal-"), ".jsonl")
		if day < cutoff {
			if err := os.Remove(f); err == nil {
				log.Printf("[JOURNAL] Removed %s (older than %d days)", filepath.Base(f), j.keepDays)
			}
		}
	}
}

// LastSeen returns the arrival time of the newest entry of each kind.
func (j *Journal) LastSeen() map[string]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make(map[string]string, len(j.lastSeen))
	for k, t := range j.lastSeen {
		out[k] = t.Local().Format(journalTimeFormat)
	}
	return out
}
