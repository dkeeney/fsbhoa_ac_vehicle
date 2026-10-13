package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Reading DoorKing RAM's Live Streaming "Disk File" (CLAUDE.md, "Disk File destination";
// TODO #31). RAM appends every live transaction to a file, opening it for each record, so
// the file never needs reconnecting and keeps recording while this service is down. It is
// on the NAS (CIFS), where Linux gets no change notices, so it is polled. The read position
// is saved, so a restart or reboot continues where it stopped. Once a day the file is
// renamed to stream_YYYY-MM-DD.txt (RAM starts a new one with its next record), and renamed
// files older than journal_days are deleted.

// RAMFile reads RAM's Disk File.
type RAMFile struct {
	path      string
	statePath string
	keepDays  int
	lines     *ramLines

	mu         sync.Mutex
	state      ramFileState
	lastPoll   time.Time
	lastRecord time.Time
	lastErr    string
	offset     int64 // state.Offset as last saved, for Status
	// skipExisting: there was no saved position, so this is the first start with the file as
	// the source. Records already in the file came through the stream; skip them.
	skipExisting bool
}

// ramFileState is saved after each read.
type ramFileState struct {
	Inode  uint64 `json:"inode"`  // the file the offset belongs to
	Offset int64  `json:"offset"` // bytes read, to the end of the last complete line
	Day    string `json:"day"`    // local day this file was first seen; it is rotated on a later day
}

const (
	ramFilePoll    = 500 * time.Millisecond
	ramFileMaxRead = 1 << 20 // bytes per poll; a backlog is read over several polls
)

// RAM opens the file for each record, so one may land in the old file just after the
// rename; wait this long before the last read of the renamed file. A variable for the tests.
var ramFileRotateSettle = 2 * time.Second

func NewRAMFile(path, stateDir string, keepDays int, l *RAMListener) *RAMFile {
	return &RAMFile{
		path:      path,
		statePath: filepath.Join(stateDir, "ram_file_state.json"),
		keepDays:  keepDays,
		lines:     &ramLines{l: l, tag: map[string]interface{}{"source": "file"}, from: path, output: true},
	}
}

// Run polls the file until the process ends.
func (f *RAMFile) Run() {
	f.load()
	log.Printf("DoorKing RAM Disk File: reading %s from byte %d", f.path, f.state.Offset)
	for {
		f.poll(time.Now())
		time.Sleep(ramFilePoll)
	}
}

func (f *RAMFile) poll(now time.Time) {
	f.mu.Lock()
	f.lastPoll = now
	f.mu.Unlock()

	fi, err := os.Stat(f.path)
	if os.IsNotExist(err) {
		f.skipExisting = false // when it appears, all of it is new
		f.setErr(nil)          // RAM creates it with its next record
		return
	}
	if err != nil {
		f.setErr(err) // the NAS may be unreachable; try again at the next poll
		return
	}
	ino := inodeOf(fi)
	if f.skipExisting {
		f.skipExisting = false
		f.state = ramFileState{Inode: ino, Offset: fi.Size(), Day: now.Format("2006-01-02")}
		f.save()
		log.Printf("[RAMFILE] First start reading %s: skipped the %d bytes already in it (the stream was the source until now)", f.path, fi.Size())
		return
	}
	if ino != f.state.Inode || fi.Size() < f.state.Offset {
		if f.state.Inode != 0 {
			log.Printf("[RAMFILE] %s is a new file; reading it from the start", f.path)
		}
		f.state = ramFileState{Inode: ino, Day: now.Format("2006-01-02")}
		f.save()
	}
	if fi.Size() > f.state.Offset {
		if err := f.read(f.path); err != nil {
			f.setErr(err)
			return
		}
	}
	f.setErr(nil)
	if f.state.Day != now.Format("2006-01-02") && f.state.Offset > 0 {
		f.rotate(now)
	}
}

// read processes the complete lines added to path since the saved offset.
func (f *RAMFile) read(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(f.state.Offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, ramFileMaxRead)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	buf = buf[:n]
	end := strings.LastIndexByte(string(buf), '\n')
	if end < 0 {
		return nil // a record still being written; wait for its line end
	}
	// Arrival time is when the line is read. After an outage a backlog arrives at once, and
	// the record's own date and time (whole minutes) tell when it happened.
	at := time.Now()
	got := 0
	for _, line := range strings.SplitAfter(string(buf[:end+1]), "\n") {
		if _, ok := f.lines.Line(line, at); ok {
			got++
		}
	}
	f.lines.Flush()
	f.state.Offset += int64(end + 1)
	f.save()
	if got > 0 {
		f.mu.Lock()
		f.lastRecord = at
		f.mu.Unlock()
	}
	return nil
}

// rotate renames the file to stream_<day>.txt after reading all of it. RAM starts a new
// file with its next record.
func (f *RAMFile) rotate(now time.Time) {
	dir := filepath.Dir(f.path)
	rotated := filepath.Join(dir, "stream_"+f.state.Day+".txt")
	for i := 2; fileExists(rotated); i++ {
		rotated = filepath.Join(dir, fmt.Sprintf("stream_%s-%d.txt", f.state.Day, i))
	}
	if err := os.Rename(f.path, rotated); err != nil {
		log.Printf("[RAMFILE] Could not rotate %s: %v; trying again at the next poll", f.path, err)
		return
	}
	time.Sleep(ramFileRotateSettle)
	if err := f.read(rotated); err != nil {
		log.Printf("[RAMFILE] Reading the rest of %s: %v", rotated, err)
	}
	if fi, err := os.Stat(rotated); err == nil && fi.Size() > f.state.Offset {
		log.Printf("[RAMFILE] %s ends with %d bytes that aren't a complete record; skipped", rotated, fi.Size()-f.state.Offset)
	}
	log.Printf("[RAMFILE] Rotated %s to %s", f.path, filepath.Base(rotated))
	f.state = ramFileState{Day: now.Format("2006-01-02")}
	f.save()
	f.purge(now)
}

// purge deletes rotated files older than keepDays.
func (f *RAMFile) purge(now time.Time) {
	files, err := filepath.Glob(filepath.Join(filepath.Dir(f.path), "stream_*.txt"))
	if err != nil {
		return
	}
	sort.Strings(files)
	cutoff := now.AddDate(0, 0, -f.keepDays).Format("2006-01-02")
	for _, name := range files {
		day := strings.TrimPrefix(filepath.Base(name), "stream_")
		if len(day) < 10 || day[:10] >= cutoff {
			continue
		}
		if err := os.Remove(name); err == nil {
			log.Printf("[RAMFILE] Removed %s (older than %d days)", filepath.Base(name), f.keepDays)
		}
	}
}

func (f *RAMFile) load() {
	data, err := os.ReadFile(f.statePath)
	if os.IsNotExist(err) {
		f.skipExisting = true
		return
	}
	if err != nil {
		log.Printf("[RAMFILE] Could not read %s (%v); reading the file from the start", f.statePath, err)
		return
	}
	if err := json.Unmarshal(data, &f.state); err != nil {
		log.Printf("[RAMFILE] Could not parse %s (%v); reading the file from the start", f.statePath, err)
		f.state = ramFileState{}
	}
}

func (f *RAMFile) save() {
	f.mu.Lock()
	f.offset = f.state.Offset
	f.mu.Unlock()
	data, _ := json.Marshal(f.state)
	tmp := f.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		log.Printf("[RAMFILE] Could not save the read position: %v", err)
		return
	}
	if err := os.Rename(tmp, f.statePath); err != nil {
		log.Printf("[RAMFILE] Could not save the read position: %v", err)
	}
}

// setErr records the current error, logging it when it starts and when it clears.
func (f *RAMFile) setErr(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	f.mu.Lock()
	prev := f.lastErr
	f.lastErr = msg
	f.mu.Unlock()
	if msg != "" && prev == "" {
		log.Printf("[RAMFILE] Can't read %s: %v; retrying", f.path, err)
	} else if msg == "" && prev != "" {
		log.Printf("[RAMFILE] %s is readable again", f.path)
	}
}

// Status describes the reader, for the health report.
func (f *RAMFile) Status() map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := map[string]interface{}{"path": f.path, "offset": f.offset}
	if !f.lastPoll.IsZero() {
		st["last_poll_at"] = f.lastPoll.Local().Format(journalTimeFormat)
	}
	if !f.lastRecord.IsZero() {
		st["last_record_at"] = f.lastRecord.Local().Format(journalTimeFormat)
	}
	if f.lastErr != "" {
		st["error"] = f.lastErr
	}
	return st
}

// ramFileAllowed reports whether the file may be read on this server: the environment must
// be valid and the file's folder name must contain it (Live_Testbed, Live_Production), so a
// server never reads the other environment's transactions into its vehicle log.
func ramFileAllowed(path, environment string) error {
	if !validEnvironment(environment) {
		return fmt.Errorf("the environment is %q, not testbed or production", environment)
	}
	folder := filepath.Base(filepath.Dir(path))
	if !strings.Contains(strings.ToLower(folder), environment) {
		return fmt.Errorf("its folder %q doesn't contain the environment name %q", folder, environment)
	}
	return nil
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
