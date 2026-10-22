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
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(time.Second), after)
	u.Read(lane, now.Add(100*time.Millisecond))
	u.Read(lane, now.Add(-time.Second)) // 4 s before the next car entered "beyond", which claims it
	next := now.Add(3 * time.Second)
	u.Car(cam, lane, next.Add(-time.Second), next, next.Add(time.Second), after)
	time.Sleep(after + 4*time.Second)
	if got := r.got(); len(got) != 0 {
		t.Fatalf("read cars reported as unread: %+v", got)
	}

	// A car with no read: reported once its window and the settle time have passed
	u, r = newTestUnread()
	now = time.Now()
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(500*time.Millisecond), after)
	// The same car counted again just after it left "beyond": not a second car
	u.Car(cam, lane, now.Add(time.Second), now.Add(time.Second), now.Add(1500*time.Millisecond), after)
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
	u.Car(cam, lane, now.Add(-time.Second), now, now.Add(time.Second), after)
	u.Car(cam, lane, second.Add(-time.Second), second, second.Add(time.Second), after)
	u.Read(lane, now.Add(50*time.Millisecond))
	time.Sleep(3*time.Second + after + 300*time.Millisecond)
	if got := r.got(); len(got) != 1 || !got[0].start.Equal(second) {
		t.Fatalf("want the second car unread, got %+v", got)
	}
}
