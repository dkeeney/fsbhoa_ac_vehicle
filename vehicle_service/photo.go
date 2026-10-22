package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OverviewPhotos keeps the last few seconds of snapshots from each lane's overview camera,
// so a plate read, which arrives 1 to 7 seconds after the car passed, can be sent with a
// photo of the car at the gate (TODO #35). The camera is asked for a snapshot continuously
// (twice a second, or as fast as it answers) and the frame nearest the read's capture time,
// less overviewPhotoLead, goes with the event. Read-only: it only fetches snapshots.
//
// Two kinds of camera: Speco (O2VLB5), whose /snapshot.JPG needs no login, and Reolink
// (South Enter's RLC-810A), whose Snap command does, at 640 x 360 from its second stream
// (0.3 s; any larger size is the full 3840 x 2160, which takes 4 s). A camera is tried as a
// Speco first, so the Reolink login is only ever sent to a camera that refused that.
type OverviewPhotos struct {
	mu        sync.Mutex
	watches   map[string]*photoWatch // by camera IP
	lanes     atomic.Pointer[map[string]OverviewLane]
	loginFile string
	onFrame   func(ip string, f photoFrame) // each new frame, such as for counting cars; nil: none
}

const (
	// A lane's lead when its "Photo lead" setting is blank (the same as PHOTO_LEAD_DEFAULT in
	// the settings page). The car is fully in view at the gate this long before the LPR camera's capture time:
	// at South Enter's Reolink, 3.5 to 4.5 s before (2026-10-20, two cars), and gone 1.5 s
	// before. The LPR cameras' clocks are part of this, so it shifts if one drifts.
	overviewPhotoLead = 4 * time.Second
	// A frame further than this from the moment wanted isn't the car's photo.
	overviewPhotoMaxGap = 1500 * time.Millisecond
	// Frames are kept this long: up to about 17 s until a read is sent (the photos arrive 1 to
	// 7 s after the capture, and wait up to 10 s for each other), plus the lead, or for "auto"
	// the 10 s it looks in and the 20 s or so of frames before that it takes the lane's
	// background from.
	overviewPhotoKeep = 45 * time.Second
	// "auto": the frame with the most car in it, from this long before the capture to
	// overviewAutoAfter after it, against the median of the frames up to the capture.
	overviewAutoLook      = 10 * time.Second
	overviewAutoAfter     = 500 * time.Millisecond
	overviewAutoMin       = 0.01 // less than this share of the picture differing is no car: fall back to the lead
	overviewPhotoInterval = 500 * time.Millisecond
	overviewPhotoRetry    = 2 * time.Second // after a second failure in a row, before trying again; after one, it tries again at once
	// A snapshot takes 0.3 s (Reolink) to 1 s (Speco). The Reolink now and then doesn't answer
	// (5 times in 75 minutes on 2026-10-20), and with a 5 s timeout and a wait after it, 3 of
	// 34 South Enter reads had no frame near their moment.
	overviewPhotoTimeout  = 3 * time.Second
	overviewPhotoMaxWidth = 1000 // larger snapshots (Speco, 1920 x 1080) are halved before sending
	overviewPhotoQuality  = 80

	// The cameras' passwords file (CLAUDE.md, "Plate reads: FTP from the cameras"), owned by
	// the service's user. The Reolink login is its "All other cameras: <user> <password>" line.
	cameraLoginFile   = ".config/fsbhoa/lpr_ftp"
	reolinkLoginLabel = "All other cameras:"
)

type photoFrame struct {
	at   time.Time
	data []byte
}

type photoWatch struct {
	ip     string
	stop   chan struct{}
	mu     sync.Mutex
	frames []photoFrame // oldest first
}

func NewOverviewPhotos() *OverviewPhotos {
	o := &OverviewPhotos{watches: map[string]*photoWatch{}}
	if home, err := os.UserHomeDir(); err == nil {
		o.loginFile = filepath.Join(home, cameraLoginFile)
	}
	return o
}

// SetFrameHandler sets a function given each new frame, on the camera's own goroutine. Call
// before SetGates.
func (o *OverviewPhotos) SetFrameHandler(h func(ip string, f photoFrame)) { o.onFrame = h }

// SetGates watches each lane's overview camera, and its arm camera (frames for the car count
// and the arm, not photos), and stops watching a camera no lane has. Applied without a restart.
func (o *OverviewPhotos) SetGates(g *Gates) {
	lanes := g.OverviewCameras()
	o.lanes.Store(&lanes)
	want := map[string]bool{}
	for _, l := range lanes {
		want[l.IP] = true
	}
	for _, ip := range g.ArmCameras() {
		want[ip] = true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for ip, w := range o.watches {
		if !want[ip] {
			close(w.stop)
			delete(o.watches, ip)
			log.Printf("[PHOTO] Stopped keeping overview frames from %s", ip)
		}
	}
	for ip := range want {
		if _, running := o.watches[ip]; !running {
			w := &photoWatch{ip: ip, stop: make(chan struct{})}
			o.watches[ip] = w
			go o.run(w)
			log.Printf("[PHOTO] Keeping overview frames from %s", ip)
		}
	}
}

// ForRead returns the overview photo for a plate read at a lane, ready to send, and the time
// it was taken; nil if the lane has no overview camera or there is no frame near enough.
func (o *OverviewPhotos) ForRead(lane, direction string, capture time.Time) ([]byte, time.Time) {
	lanes := o.lanes.Load()
	if lanes == nil || capture.IsZero() {
		return nil, time.Time{}
	}
	l := (*lanes)[direction+" "+lane]
	ip := l.IP
	o.mu.Lock()
	w := o.watches[ip]
	o.mu.Unlock()
	if w == nil {
		return nil, time.Time{}
	}
	var f photoFrame
	var ok bool
	if l.Auto {
		f, ok = w.mostCar(capture)
	}
	if !ok {
		f, ok = w.nearest(capture.Add(-l.Lead))
	}
	if !ok {
		log.Printf("[PHOTO] No overview frame from %s near %s for %s %s", ip, capture.Format("15:04:05.000"), direction, lane)
		return nil, time.Time{}
	}
	return sendable(ip, f.data), f.at
}

// Near returns a camera's kept frame nearest t, ready to send, and the time it was taken; nil
// if the camera isn't watched or no frame is within overviewPhotoMaxGap.
func (o *OverviewPhotos) Near(ip string, t time.Time) ([]byte, time.Time) {
	o.mu.Lock()
	w := o.watches[ip]
	o.mu.Unlock()
	if w == nil {
		return nil, time.Time{}
	}
	f, ok := w.nearest(t)
	if !ok {
		log.Printf("[PHOTO] No overview frame from %s near %s", ip, t.Format("15:04:05.000"))
		return nil, time.Time{}
	}
	return sendable(ip, f.data), f.at
}

// sendable halves a frame wider than overviewPhotoMaxWidth (the Speco cameras' 1920 x 1080).
func sendable(ip string, data []byte) []byte {
	if c, err := jpeg.DecodeConfig(bytes.NewReader(data)); err == nil && c.Width > overviewPhotoMaxWidth {
		if small, err := shrinkJPEG(data, 2, overviewPhotoQuality); err == nil {
			return small
		}
		log.Printf("[WARN] Overview photo from %s: cannot shrink; sending it as it is", ip)
	}
	return data
}

// Latest returns the newest frame from a camera, and whether the camera is watched at all.
// Safe on a nil OverviewPhotos (nothing is watched).
func (o *OverviewPhotos) Latest(ip string) (photoFrame, bool) {
	if o == nil {
		return photoFrame{}, false
	}
	o.mu.Lock()
	w := o.watches[ip]
	o.mu.Unlock()
	if w == nil {
		return photoFrame{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.frames) == 0 {
		return photoFrame{}, true
	}
	return w.frames[len(w.frames)-1], true
}

// nearest returns the kept frame closest to t, if one is within overviewPhotoMaxGap.
func (w *photoWatch) nearest(t time.Time) (photoFrame, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var best photoFrame
	var bestGap time.Duration = -1
	for _, f := range w.frames {
		gap := f.at.Sub(t)
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = f, gap
		}
	}
	return best, bestGap >= 0 && bestGap <= overviewPhotoMaxGap
}

// mostCar returns the frame, from overviewAutoLook before the capture to overviewAutoAfter
// after it, that differs most from the lane's background (each pixel's median over the kept
// frames up to the capture): the car the camera saw best. Found 2026-10-20 on the North and
// South exit lanes and North Enter, where cars wait a varying time (at a keypad, at a gate)
// before the plate is read. A car that waited longer than the background's span is part of
// it, and isn't found.
func (w *photoWatch) mostCar(capture time.Time) (photoFrame, bool) {
	w.mu.Lock()
	frames := append([]photoFrame(nil), w.frames...)
	w.mu.Unlock()

	var history, look [][]uint8
	var lookFrames []photoFrame
	for _, f := range frames {
		if f.at.After(capture.Add(overviewAutoAfter)) {
			break
		}
		img, err := jpeg.Decode(bytes.NewReader(f.data))
		if err != nil {
			continue
		}
		g := reduceTo(img, autoWidth, autoHeight)
		if !f.at.After(capture) {
			history = append(history, g)
		}
		if !f.at.Before(capture.Add(-overviewAutoLook)) {
			look, lookFrames = append(look, g), append(lookFrames, f)
		}
	}
	if len(history) < 3 || len(look) == 0 {
		return photoFrame{}, false
	}
	bg := medianFrame(history)
	best, bestScore := -1, 0.0
	for i, g := range look {
		if s := differingShare(g, bg); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 || bestScore < overviewAutoMin {
		return photoFrame{}, false
	}
	return lookFrames[best], true
}

func (w *photoWatch) add(f photoFrame) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, f)
	cutoff := f.at.Add(-overviewPhotoKeep)
	drop := 0
	for drop < len(w.frames) && w.frames[drop].at.Before(cutoff) {
		drop++
	}
	w.frames = append(w.frames[:0:0], w.frames[drop:]...)
}

// run fetches snapshots until the watch is stopped. A frame's time is the middle of its
// request, since the camera takes the picture somewhere in between.
func (o *OverviewPhotos) run(w *photoWatch) {
	client := &http.Client{Timeout: overviewPhotoTimeout}
	reolink := false // whether the camera is a Reolink (decided by the first answer)
	failing := false
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		start := time.Now()
		var data []byte
		var err error
		if reolink {
			data, err = o.reolinkSnap(client, w.ip)
		} else {
			data, err = fetchSnapshot(client, "http://"+w.ip+"/snapshot.JPG")
			if err != nil && strings.HasPrefix(err.Error(), "HTTP 404") {
				if data, err = o.reolinkSnap(client, w.ip); err == nil {
					reolink = true
					log.Printf("[PHOTO] %s is a Reolink camera", w.ip)
				}
			}
		}
		end := time.Now()
		wait := overviewPhotoInterval - end.Sub(start)
		if err != nil {
			if !failing {
				log.Printf("[PHOTO] %s: snapshot failed: %v", w.ip, err)
				failing = true
				wait = 0 // a single miss: try again at once
			} else {
				wait = overviewPhotoRetry
			}
		} else {
			if failing {
				log.Printf("[PHOTO] %s: snapshots working again", w.ip)
				failing = false
			}
			f := photoFrame{at: start.Add(end.Sub(start) / 2), data: data}
			w.add(f)
			if o.onFrame != nil {
				o.onFrame(w.ip, f)
			}
		}
		if wait > 0 {
			select {
			case <-w.stop:
				return
			case <-time.After(wait):
			}
		}
	}
}

// reolinkSnap fetches a 640 x 360 snapshot from a Reolink camera, with the login from the
// cameras' passwords file. Errors never include the URL, which holds the password.
func (o *OverviewPhotos) reolinkSnap(client *http.Client, ip string) ([]byte, error) {
	user, password, err := o.reolinkLogin()
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("cmd", "Snap")
	q.Set("channel", "0")
	q.Set("rs", strconv.FormatInt(time.Now().UnixNano(), 36)) // Reolink wants a random string, against caching
	q.Set("width", "640")
	q.Set("height", "360")
	q.Set("user", user)
	q.Set("password", password)
	data, err := fetchSnapshot(client, "http://"+ip+"/cgi-bin/api.cgi?"+q.Encode())
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if err != nil {
		return nil, err
	}
	// A refused login is answered 200 with a JSON error instead of a JPEG
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, fmt.Errorf("not a JPEG: %.120s", data)
	}
	return data, nil
}

// reolinkLogin reads the Reolink login from the passwords file. Read each time it is needed,
// so a changed password is picked up without a restart.
func (o *OverviewPhotos) reolinkLogin() (string, string, error) {
	if o.loginFile == "" {
		return "", "", errors.New("no home folder for the cameras' passwords file")
	}
	f, err := os.Open(o.loginFile)
	if err != nil {
		return "", "", fmt.Errorf("cameras' passwords file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), reolinkLoginLabel); ok {
			// The password is everything after the user, as the shell's `read -r user password` takes it
			if user, password, ok := strings.Cut(strings.TrimSpace(rest), " "); ok && strings.TrimSpace(password) != "" {
				return user, strings.TrimSpace(password), nil
			}
		}
	}
	return "", "", fmt.Errorf("no %q line with a user and a password in %s", reolinkLoginLabel, o.loginFile)
}
