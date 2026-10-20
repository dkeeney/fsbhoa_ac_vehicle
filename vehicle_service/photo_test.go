package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPhotoWatchKeepsAndPicks(t *testing.T) {
	w := &photoWatch{}
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	for i := 0; i <= 80; i++ { // 40 s of frames, twice a second
		w.add(photoFrame{at: t0.Add(time.Duration(i) * 500 * time.Millisecond), data: []byte{byte(i)}})
	}
	if n := len(w.frames); n != 61 {
		t.Fatalf("kept %d frames, want the last 30 s (61)", n)
	}
	f, ok := w.nearest(t0.Add(35*time.Second + 200*time.Millisecond))
	if !ok || f.data[0] != 70 {
		t.Fatalf("nearest = %v %v, want frame 70", f.data, ok)
	}
	if _, ok := w.nearest(t0.Add(5 * time.Second)); ok {
		t.Fatal("picked a frame for a moment older than the kept frames")
	}
	if _, ok := w.nearest(t0.Add(45 * time.Second)); ok {
		t.Fatal("picked a frame 5 s from the moment wanted")
	}
}

func TestOverviewPhotosForRead(t *testing.T) {
	o := NewOverviewPhotos()
	lanes := map[string]string{"entry South Enter": "192.168.107.141"}
	o.lanes.Store(&lanes)
	capture := time.Date(2026, 10, 20, 9, 0, 56, 217e6, time.Local)
	w := &photoWatch{ip: "192.168.107.141"}
	w.add(photoFrame{at: capture.Add(-5 * time.Second), data: testJPEG(t, 640, 360)})
	big := testJPEG(t, 1920, 1080)
	w.add(photoFrame{at: capture.Add(-3900 * time.Millisecond), data: big})
	w.add(photoFrame{at: capture.Add(-1 * time.Second), data: testJPEG(t, 640, 360)})
	o.watches["192.168.107.141"] = w

	data, at := o.ForRead("South Enter", "entry", capture)
	if data == nil || !at.Equal(capture.Add(-3900*time.Millisecond)) {
		t.Fatalf("ForRead took the frame at %v, want 3.9 s before the capture", at)
	}
	c, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || c.Width != 960 {
		t.Fatalf("photo is %d wide (%v), want a 1920-wide frame halved", c.Width, err)
	}
	if data, _ := o.ForRead("North Enter", "entry", capture); data != nil {
		t.Fatal("a photo for a lane without an overview camera")
	}
}

func TestReolinkLogin(t *testing.T) {
	o := NewOverviewPhotos()
	o.loginFile = filepath.Join(t.TempDir(), "lpr_ftp")
	os.WriteFile(o.loginFile, []byte("FTP Server Password: x\nSpeco Cameras:   admin s1\nAll other cameras: admin p@ss word \n"), 0600)
	user, password, err := o.reolinkLogin()
	if err != nil || user != "admin" || password != "p@ss word" {
		t.Fatalf("login = %q %q %v", user, password, err)
	}
	os.WriteFile(o.loginFile, []byte("Speco Cameras: admin s1\n"), 0600)
	if _, _, err := o.reolinkLogin(); err == nil {
		t.Fatal("no error without the line")
	}
}

// A camera that refuses /snapshot.JPG is asked as a Reolink, with the login; a refused
// Reolink login (a JSON answer) is an error that doesn't show the password.
func TestOverviewPhotosReolink(t *testing.T) {
	photo := testJPEG(t, 640, 360)
	var mu sync.Mutex // the handler runs on the server's goroutines
	var gotUser, gotPassword string
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cgi-bin/api.cgi" || r.URL.Query().Get("cmd") != "Snap" {
			http.NotFound(rw, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		gotUser, gotPassword = r.URL.Query().Get("user"), r.URL.Query().Get("password")
		if refuse {
			rw.Write([]byte(`[{"cmd":"Snap","code":1,"error":{"detail":"please login first","rspCode":-6}}]`))
			return
		}
		rw.Write(photo)
	}))
	defer srv.Close()

	o := NewOverviewPhotos()
	o.loginFile = filepath.Join(t.TempDir(), "lpr_ftp")
	os.WriteFile(o.loginFile, []byte("All other cameras: admin secret\n"), 0600)
	ip := strings.TrimPrefix(srv.URL, "http://")
	w := &photoWatch{ip: ip, stop: make(chan struct{})}
	go o.run(w)
	deadline := time.Now().Add(3 * time.Second)
	for {
		w.mu.Lock()
		n := len(w.frames)
		w.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no frame from the fake Reolink")
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(w.stop)
	mu.Lock()
	if gotUser != "admin" || gotPassword != "secret" {
		t.Fatalf("login sent: %q %q", gotUser, gotPassword)
	}
	refuse = true
	mu.Unlock()
	_, err := o.reolinkSnap(&http.Client{Timeout: time.Second}, ip)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("refused login: err = %v", err)
	}
	srv.Close()
	_, err = o.reolinkSnap(&http.Client{Timeout: time.Second}, ip)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unreachable camera: err = %v", err)
	}
}
