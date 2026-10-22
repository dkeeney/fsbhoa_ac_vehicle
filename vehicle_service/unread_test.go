package main

import (
	"sync"
	"testing"
	"time"
)

// unreadRecorder collects the cars UnreadCars reports.
type unreadRecorder struct {
	mu   sync.Mutex
	cars []unreadCar
}

func (r *unreadRecorder) got() []unreadCar {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]unreadCar(nil), r.cars...)
}

func newTestUnread() (*UnreadCars, *unreadRecorder) {
	r := &unreadRecorder{}
	u := NewUnreadCars(nil)
	u.settle = 50 * time.Millisecond
	u.SetSend(func(c unreadCar) {
		r.mu.Lock()
		r.cars = append(r.cars, c)
		r.mu.Unlock()
	})
	return u, r
}

func TestUnreadCars(t *testing.T) {
	const after = 200 * time.Millisecond
	cam, lane := "192.168.107.141", "South Enter"

	// A car read within its window, the read arriving after the count or before it
	u, r := newTestUnread()
	now := time.Now()
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(time.Second), after, "", nil, true)
	u.Read(lane, now.Add(100*time.Millisecond))
	u.Read(lane, now.Add(-time.Second)) // 4 s before the next car entered "beyond", which claims it
	next := now.Add(3 * time.Second)
	u.Car(cam, lane, next.Add(-time.Second), next, next.Add(time.Second), after, "", nil, true)
	time.Sleep(after + 4*time.Second)
	if got := r.got(); len(got) != 0 {
		t.Fatalf("read cars reported as unread: %+v", got)
	}

	// A car with no read: reported once its window and the settle time have passed
	u, r = newTestUnread()
	now = time.Now()
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(500*time.Millisecond), after, "", nil, true)
	// The same car counted again just after it left "beyond": not a second car
	u.Car(cam, lane, now.Add(time.Second), now.Add(time.Second), now.Add(1500*time.Millisecond), after, "", nil, true)
	// A read at another lane, and one too late for the window
	u.Read("North Enter", now)
	u.Read(lane, now.Add(after+100*time.Millisecond))
	time.Sleep(after + 300*time.Millisecond)
	got := r.got()
	if len(got) != 1 || !got[0].start.Equal(now) || !got[0].end.Equal(now.Add(1500*time.Millisecond)) || got[0].gate != lane {
		t.Fatalf("want one unread car from %v to %v, got %+v", now, now.Add(1500*time.Millisecond), got)
	}

	// Two cars and one read: the first claims it, the second is unread
	u, r = newTestUnread()
	now = time.Now()
	second := now.Add(3 * time.Second)
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(time.Second), after, "", nil, true)
	u.Car(cam, lane, second.Add(-time.Second), second, second.Add(time.Second), after, "", nil, true)
	u.Read(lane, now.Add(50*time.Millisecond))
	time.Sleep(3*time.Second + after + 300*time.Millisecond)
	if got := r.got(); len(got) != 1 || !got[0].start.Equal(second) {
		t.Fatalf("want the second car unread, got %+v", got)
	}
}

func TestUnreadCarsNote(t *testing.T) {
	u, r := newTestUnread()
	now := time.Now()
	u.Car("192.168.107.141", "South Enter", now.Add(-time.Second), now, now.Add(time.Second), time.Second, "Arm already up 2 min 56 s", []byte("photo"), true)
	if c := u.Read("South Enter", now.Add(3*time.Second)); c.note != "" || c.notePhoto != nil {
		t.Fatalf("a read outside the window got the note: %q", c.note)
	}
	if c := u.Read("South Enter", now.Add(500*time.Millisecond)); c.note != "Arm already up 2 min 56 s" || string(c.notePhoto) != "photo" || c.start.IsZero() {
		t.Fatalf("the car's read: %+v", c)
	}
	time.Sleep(1200 * time.Millisecond)
	if got := r.got(); len(got) != 0 {
		t.Fatalf("a read car was reported unread: %+v", got)
	}
}

// A car the counter says backed out is sent only when no plate was read around it: a read
// means it drove in and the counter missed it (2026-10-22, white cars at South Enter).
func TestUnreadBacked(t *testing.T) {
	const after = 200 * time.Millisecond
	cam, lane := "192.168.107.141", "South Enter"
	var mu sync.Mutex
	sent := 0
	u := NewUnreadCars(nil)
	u.settle = 50 * time.Millisecond
	u.SetSendBacked(func(camera, gate string, e countEvent, photoAt time.Time) {
		mu.Lock()
		sent++
		mu.Unlock()
	})
	now := time.Now()

	// Read a moment after it left "approach": it drove in, not sent
	u.Backed(cam, lane, countEvent{approach: now.Add(-2 * time.Second), end: now}, after, now)
	u.Read(lane, now.Add(100*time.Millisecond))
	time.Sleep(after + 300*time.Millisecond)
	mu.Lock()
	if sent != 0 {
		t.Fatalf("a back-out with a plate read was sent")
	}
	mu.Unlock()

	// No read (another lane, so that read isn't near it): sent
	later := time.Now()
	u.Backed(cam, "North Enter", countEvent{approach: later.Add(-2 * time.Second), end: later}, after, later)
	time.Sleep(after + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if sent != 1 {
		t.Fatalf("a back-out with no plate read was sent %d times, want 1", sent)
	}
}

// A car matched only for its read's photo (photo=) is never reported as unread, and its read
// learns when it entered "beyond".
func TestUnreadPhotoOnly(t *testing.T) {
	const after = 200 * time.Millisecond
	u, r := newTestUnread()
	now := time.Now()
	u.Car("192.168.107.10", "North Enter", now.Add(-time.Second), now, now.Add(time.Second), after, "", nil, false)
	time.Sleep(after + 300*time.Millisecond)
	if got := r.got(); len(got) != 0 {
		t.Fatalf("a photo-only car was reported unread: %+v", got)
	}
	next := time.Now().Add(10 * time.Second) // well after the first car, not a second count of it
	u.Car("192.168.107.10", "North Enter", next.Add(-time.Second), next, next.Add(time.Second), 5*time.Second, "", nil, false)
	if c := u.Read("North Enter", next.Add(3*time.Second)); c.camera != "192.168.107.10" || !c.start.Equal(next) {
		t.Fatalf("the read didn't get its car: %+v", c)
	}
}
