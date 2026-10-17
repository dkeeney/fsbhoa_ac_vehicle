package main

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// LPR camera uploads (CLAUDE.md, "Plate reads: FTP from the cameras"). Each Speco camera
// uploads over FTP (vsftpd, lpr_ftp/) into <drop>/<camera MAC>/VEHICE/<date>/<hour>/:
//   - VEHICE_<capture time>_plate_<PLATE>.jpg  the plate crop
//   - VEHICE_<capture time>_src.jpg            the scene photo
//   - VEHICE_<date>-<hour>.txt                 the hour's summary, one line per read, with
//                                              the camera's name, MAC and address
// The photos are uploaded as .tmp and renamed, so a final name is a complete file. The plate
// crop arrives about a second before the scene photo.
//
// For the traffic study each read is journaled ("plate"); nothing is correlated or sent yet,
// and the files are left in place (lpr_ftp/ deletes them after 7 days).

var (
	lprPlateFile   = regexp.MustCompile(`^VEHICE_(\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d+)_plate_(.*)\.jpg$`)
	lprSceneFile   = regexp.MustCompile(`^VEHICE_(\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d+)_src\.jpg$`)
	lprSummaryFile = regexp.MustCompile(`^VEHICE_\d{4}-\d{2}-\d{2}-\d{2}\.txt$`)
	// device name: S Gate Entry LPR mac: 5c-f2-07-5f-fd-97  ipaddr:192.168.107.138, License plate number:...
	lprSummaryLine = regexp.MustCompile(`device name:\s*(.*?)\s+mac:\s*(\S+)\s+ipaddr:\s*([0-9.]+)`)
	lprCaptureTime = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-(\d{2})-(\d{2})-(\d{2})-(\d+)$`)
	// A vehicle without a readable plate ("Capture Plate Absence Vehicle"): the plate is
	// Unknown000 in the file name (Unknown in the summary), and the crop is the whole vehicle
	lprNoPlate = regexp.MustCompile(`^Unknown\d*$`)
	// lprCaptureTimeText's result, as /vehicle-event takes it
	lprEventTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{1,6}$`)
)

// lprPairWait is how long a plate crop waits for its scene photo, and a scene photo for its
// plate crop, before the read is journaled with the other one missing.
const lprPairWait = 10 * time.Second

// lprCameraWait is how long a complete read from a camera not known yet waits for the
// camera's hourly summary, which names it. The summary is uploaded after the photos.
const lprCameraWait = 3 * time.Second

// LPRDrop watches the upload folder and journals each read.
type LPRDrop struct {
	dir        string
	journal    *Journal
	debugf     func(format string, args ...interface{})
	pairWait   time.Duration
	cameraWait time.Duration
	gates      atomic.Pointer[Gates]
	events     *PlateEvents // sends reads to the vehicle log when that setting is on; may be nil

	mu      sync.Mutex
	cameras map[string]lprCamera // by MAC folder name, learned from the hourly summaries
	pending map[string]*lprRead  // by MAC folder + capture time
	handled map[string]time.Time // files already handled (firstTime)
}

type lprCamera struct {
	Name string
	IP   string
}

// lprRead is one capture: its plate crop and scene photo, as they arrive.
type lprRead struct {
	camera      string // MAC folder name
	captureTime string // from the file name
	plate       string
	plateFile   string
	sceneFile   string
	plateAt     time.Time
	sceneAt     time.Time
	timer       *time.Timer
	waited      bool // already waited for the camera's summary
}

func NewLPRDrop(dir string, journal *Journal, debugf func(string, ...interface{})) *LPRDrop {
	return &LPRDrop{
		dir:        dir,
		journal:    journal,
		debugf:     debugf,
		pairWait:   lprPairWait,
		cameraWait: lprCameraWait,
		cameras:    map[string]lprCamera{},
		pending:    map[string]*lprRead{},
		handled:    map[string]time.Time{},
	}
}

// SetGates replaces the lane tables used to tag reads with their lane.
func (d *LPRDrop) SetGates(g *Gates) {
	d.gates.Store(g)
}

// SetEvents sets where reads are sent as vehicle events. Call before Run.
func (d *LPRDrop) SetEvents(p *PlateEvents) {
	d.events = p
}

// Run watches the folder and every folder the cameras create in it. Files already there
// when it starts are not read.
func (d *LPRDrop) Run() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[ERROR] LPR upload folder: watcher failed: %v; no plate reads will be read", err)
		return
	}
	defer watcher.Close()

	if err := os.MkdirAll(d.dir, 0775); err != nil {
		log.Printf("[WARN] LPR upload folder %s: %v", d.dir, err)
	}
	// The summaries already there name the cameras, so reads can be tagged from the start
	_ = filepath.WalkDir(d.dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && lprSummaryFile.MatchString(entry.Name()) {
			d.readSummary(path)
		}
		return nil
	})
	d.watchTree(watcher, d.dir, false)
	log.Printf("[LPR] Watching %s for camera uploads", d.dir)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			fi, err := os.Stat(event.Name)
			if err != nil {
				continue
			}
			if fi.IsDir() {
				if event.Op&fsnotify.Create != 0 {
					// A camera made a folder; files may already be in it
					d.watchTree(watcher, event.Name, true)
				}
				continue
			}
			d.handleFile(event.Name, event.Op&fsnotify.Create != 0)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[ERROR] LPR upload folder: %v", err)
		}
	}
}

// watchTree watches dir and the folders below it. With readFiles, the files already in them
// are handled too (a folder that was just created), the hourly summaries first so the
// photos' reads can be tagged with their camera.
func (d *LPRDrop) watchTree(watcher *fsnotify.Watcher, dir string, readFiles bool) {
	var summaries, others []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if err := watcher.Add(path); err != nil {
				log.Printf("[WARN] LPR upload folder: cannot watch %s: %v", path, err)
			}
		} else if readFiles {
			if lprSummaryFile.MatchString(entry.Name()) {
				summaries = append(summaries, path)
			} else {
				others = append(others, path)
			}
		}
		return nil
	})
	for _, path := range append(summaries, others...) {
		d.handleFile(path, true)
	}
}

// handleFile handles one upload. created is false for a write to an existing file, which
// matters only for the hourly summary (uploaded in place, not renamed).
func (d *LPRDrop) handleFile(path string, created bool) {
	name := filepath.Base(path)
	if strings.HasSuffix(name, ".tmp") {
		return // still uploading; the rename to the final name follows
	}
	camera := d.cameraFolder(path)

	if lprSummaryFile.MatchString(name) {
		d.readSummary(path)
		return
	}
	if !created || !d.firstTime(path) {
		return
	}
	if m := lprPlateFile.FindStringSubmatch(name); m != nil {
		d.addFile(camera, m[1], path, m[2], true)
		return
	}
	if m := lprSceneFile.FindStringSubmatch(name); m != nil {
		d.addFile(camera, m[1], path, "", false)
		return
	}

	// Anything else (a test upload, or a kind of file not seen yet) is journaled as it is
	entry := map[string]interface{}{"camera": camera, "file": path}
	if fi, err := os.Stat(path); err == nil {
		entry["size"] = fi.Size()
	}
	d.mu.Lock()
	d.tagCamera(entry, camera)
	d.mu.Unlock()
	d.journal.Write("lpr_file", entry)
	d.debugf("[LPR] Other upload from %s: %s", camera, name)
}

// firstTime reports whether path hasn't been handled yet, and marks it handled. A new
// folder's scan and its files' own notices can both report one file.
func (d *LPRDrop) firstTime(path string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if _, dup := d.handled[path]; dup {
		return false
	}
	for p, at := range d.handled {
		if now.Sub(at) > 10*time.Minute {
			delete(d.handled, p)
		}
	}
	d.handled[path] = now
	return true
}

// cameraFolder returns the first folder below the drop folder: the camera's MAC.
func (d *LPRDrop) cameraFolder(path string) string {
	rel, err := filepath.Rel(d.dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	if i := strings.IndexRune(rel, filepath.Separator); i > 0 {
		return rel[:i]
	}
	return ""
}

// addFile adds a plate crop or scene photo to its read, and journals the read once it has
// both, or when the other one hasn't come within the wait.
func (d *LPRDrop) addFile(camera, captureTime, path, plate string, isPlate bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	key := camera + "|" + captureTime
	r := d.pending[key]
	if r == nil {
		r = &lprRead{camera: camera, captureTime: captureTime}
		d.pending[key] = r
		r.timer = time.AfterFunc(d.pairWait, func() { d.finish(key) })
	}
	if isPlate {
		r.plate, r.plateFile, r.plateAt = plate, path, now
	} else {
		r.sceneFile, r.sceneAt = path, now
	}
	d.debugf("[LPR] %s %s from %s (capture %s)", map[bool]string{true: "Plate " + plate, false: "Scene photo"}[isPlate], filepath.Base(path), camera, captureTime)
	if r.plateFile != "" && r.sceneFile != "" {
		r.timer.Stop()
		if _, known := d.cameras[camera]; !known && !r.waited {
			r.waited = true
			r.timer = time.AfterFunc(d.cameraWait, func() { d.finish(key) }) // readSummary may finish it sooner
			return
		}
		d.journalRead(key, r)
	}
}

// finish journals a read whose partner file didn't come.
func (d *LPRDrop) finish(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.pending[key]; r != nil {
		d.journalRead(key, r)
	}
}

// journalRead writes the read to the journal. Call with d.mu held.
func (d *LPRDrop) journalRead(key string, r *lprRead) {
	delete(d.pending, key)
	plate := r.plate
	entry := map[string]interface{}{
		"camera":       r.camera,
		"capture_time": lprCaptureTimeText(r.captureTime),
		"plate_file":   r.plateFile,
		"scene_file":   r.sceneFile,
	}
	if !r.plateAt.IsZero() {
		entry["plate_arrived"] = r.plateAt.Format(time.RFC3339Nano)
	}
	if !r.sceneAt.IsZero() {
		entry["scene_arrived"] = r.sceneAt.Format(time.RFC3339Nano)
	}
	if lprNoPlate.MatchString(plate) {
		entry["no_plate"], entry["camera_plate"] = true, plate
		plate = ""
	}
	entry["plate"] = plate
	switch {
	case r.plateFile == "":
		entry["missing"] = "plate" // a vehicle without a readable plate, or a lost upload
	case r.sceneFile == "":
		entry["missing"] = "scene"
	}
	d.tagCamera(entry, r.camera)
	d.journal.Write("plate", entry)
	log.Printf("[LPR] Read at %v: plate %q from %s (capture %s)", entry["gate"], r.plate, r.camera, entry["capture_time"])

	if d.events != nil {
		read := PlateRead{
			Camera: r.camera, Plate: plate, CaptureTime: r.captureTime,
			PlateFile: r.plateFile, SceneFile: r.sceneFile, PlateArrived: r.plateAt, SceneArrived: r.sceneAt,
		}
		read.CameraName, _ = entry["camera_name"].(string)
		read.CameraIP, _ = entry["camera_ip"].(string)
		read.Gate, _ = entry["gate"].(string)
		read.Direction, _ = entry["direction"].(string)
		read.NoPlate, _ = entry["no_plate"].(bool)
		read.CameraPlate, _ = entry["camera_plate"].(string)
		read.Missing, _ = entry["missing"].(string)
		go d.events.Send(read) // reading and shrinking the photos takes a moment; don't hold d.mu
	}
}

// tagCamera adds the camera's name and address, and its lane when the address is in the lane
// tables. Call with d.mu held.
func (d *LPRDrop) tagCamera(entry map[string]interface{}, camera string) {
	cam, ok := d.cameras[camera]
	if !ok {
		return
	}
	entry["camera_name"], entry["camera_ip"] = cam.Name, cam.IP
	if g := d.gates.Load(); g != nil {
		if dev, ok := g.ForIP(cam.IP); ok {
			entry["gate"], entry["direction"], entry["device"] = dev.Gate, dev.Direction, dev.Device
		}
	}
}

// readSummary learns each camera's name and address from an hourly summary.
func (d *LPRDrop) readSummary(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	d.mu.Lock()
	defer d.mu.Unlock()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "\ufeff")
		m := lprSummaryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cam := lprCamera{Name: m[1], IP: m[3]}
		if d.cameras[m[2]] != cam {
			d.cameras[m[2]] = cam
			log.Printf("[LPR] Camera %s is %q at %s", m[2], cam.Name, cam.IP)
		}
	}
	// Complete reads that were waiting to learn their camera
	for key, r := range d.pending {
		if _, known := d.cameras[r.camera]; known && r.waited {
			r.timer.Stop()
			d.journalRead(key, r)
		}
	}
}

// lprCaptureTimeText turns the file name's 2026-10-17-06-31-04-333 into 2026-10-17 06:31:04.333.
func lprCaptureTimeText(s string) string {
	m := lprCaptureTime.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	return m[1] + " " + m[2] + ":" + m[3] + ":" + m[4] + "." + m[5]
}
