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
	lanes     atomic.Pointer[map[string]string]
	loginFile string
}

const (
	// The car is fully in view at the gate this long before the LPR camera's capture time:
	// at South Enter's Reolink, 3.5 to 4.5 s before (2026-10-20, two cars), and gone 1.5 s
	// before. The LPR cameras' clocks are part of this, so it shifts if one drifts.
	overviewPhotoLead = 4 * time.Second
	// A frame further than this from the moment wanted isn't the car's photo.
	overviewPhotoMaxGap = 1500 * time.Millisecond
	// Frames are kept this long: the lead, plus up to about 17 s until a read is sent (the
	// photos arrive 1 to 7 s after the capture, and wait up to 10 s for each other).
	overviewPhotoKeep     = 30 * time.Second
	overviewPhotoInterval = 500 * time.Millisecond
	overviewPhotoRetry    = 30 * time.Second // after a failure, before trying the camera again
	overviewPhotoMaxWidth = 1000             // larger snapshots (Speco, 1920 x 1080) are halved before sending
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

// SetGates watches each lane's overview camera, and stops watching a camera no lane has.
// Applied without a restart.
func (o *OverviewPhotos) SetGates(g *Gates) {
	lanes := g.OverviewCameras()
	o.lanes.Store(&lanes)
	want := map[string]bool{}
	for _, ip := range lanes {
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
	ip := (*lanes)[direction+" "+lane]
	o.mu.Lock()
	w := o.watches[ip]
	o.mu.Unlock()
	if w == nil {
		return nil, time.Time{}
	}
	f, ok := w.nearest(capture.Add(-overviewPhotoLead))
	if !ok {
		log.Printf("[PHOTO] No overview frame from %s near %s for %s %s", ip, capture.Format("15:04:05.000"), direction, lane)
		return nil, time.Time{}
	}
	data := f.data
	if c, err := jpeg.DecodeConfig(bytes.NewReader(data)); err == nil && c.Width > overviewPhotoMaxWidth {
		if small, err := shrinkJPEG(data, 2, overviewPhotoQuality); err == nil {
			data = small
		} else {
			log.Printf("[WARN] Overview photo from %s: cannot shrink (%v); sending it as it is", ip, err)
		}
	}
	return data, f.at
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
	client := &http.Client{Timeout: snapshotTimeout}
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
			}
			wait = overviewPhotoRetry
		} else {
			if failing {
				log.Printf("[PHOTO] %s: snapshots working again", w.ip)
				failing = false
			}
			w.add(photoFrame{at: start.Add(end.Sub(start) / 2), data: data})
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
