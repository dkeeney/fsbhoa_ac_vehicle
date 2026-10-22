package main

import (
	"log"
	"sync"
	"time"
)

// UnreadCars finds the cars the counter saw come in (count.go) that the lane's LPR camera
// didn't read: a car counted past the arm with no read (a plate, or a vehicle without a
// readable plate) captured from unreadPlateBefore before it entered "beyond" to its camera's
// plate= seconds after. Each is journaled ("car_unread") and, with Plate Read Events on, sent
// to the vehicle log as a vehicle with no plate and the overview photo, so the monitor has an
// entry for every car that came in (2026-10-22).
//
// Only cameras whose "beyond" zone has plate= take part: the time from the count to the read
// must be steady. At South Enter's .141 the capture is 2 to 6 s after the car enters "beyond"
// (2026-10-21 and 22); at North Enter's .10, where cars wait for the swing gate between the
// zones and the camera, it is 3 to 25 s.
type UnreadCars struct {
	mu      sync.Mutex
	lanes   map[string]*unreadLane // by lane name
	journal *Journal
	send    func(unreadCar) // sends a car to the vehicle log; nil: journal only
	settle  time.Duration
}

// unreadCar is one counted car, waiting for its read.
type unreadCar struct {
	camera, gate         string
	approach, start, end time.Time // "approach" filled, "beyond" filled and emptied
	after                time.Duration
	done                 bool // read, or reported as unread
}

type unreadLane struct {
	cars   []*unreadCar // waiting for a read, oldest first
	reads  []time.Time  // capture times of reads no car has claimed yet, oldest first
	latest *unreadCar   // the last car counted, for a second count of the same car
}

const (
	// A read captured up to this long before the car entered "beyond" is its read: the LPR
	// camera's clock against the overview camera's frames.
	unreadPlateBefore = 5 * time.Second
	// A "car" starting this soon after the last one left "beyond" is the same car counted twice
	// (2026-10-21 at dusk: a second weak count 0.6 to 1.5 s later, with one plate read).
	unreadFragmentGap = 1500 * time.Millisecond
	// After a car's window, before it is called unread: the read's files arrive 1 to 7 s after
	// the capture, and wait up to lprPairWait for each other.
	unreadSettle = 20 * time.Second
	// Reads no car claimed are kept this long, for a car counted after its read arrived.
	unreadReadKeep = 2 * time.Minute
)

func NewUnreadCars(journal *Journal) *UnreadCars {
	return &UnreadCars{lanes: map[string]*unreadLane{}, journal: journal, settle: unreadSettle}
}

// SetSend sets how an unread car is sent to the vehicle log. Call before cars arrive.
func (u *UnreadCars) SetSend(send func(unreadCar)) { u.send = send }

func (u *UnreadCars) lane(gate string) *unreadLane {
	l := u.lanes[gate]
	if l == nil {
		l = &unreadLane{}
		u.lanes[gate] = l
	}
	return l
}

func (c *unreadCar) matches(capture time.Time) bool {
	d := capture.Sub(c.start)
	return d >= -unreadPlateBefore && d <= c.after
}

// Car takes a car counted into a lane by one of its cameras whose "beyond" zone has plate=.
func (u *UnreadCars) Car(camera, gate string, approach, start, end time.Time, after time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	l := u.lane(gate)
	if p := l.latest; p != nil && p.camera == camera && !start.Before(p.start) && start.Sub(p.end) <= unreadFragmentGap {
		if end.After(p.end) {
			p.end = end
		}
		return
	}
	c := &unreadCar{camera: camera, gate: gate, approach: approach, start: start, end: end, after: after}
	l.latest = c
	// Its read may have arrived first
	for i, r := range l.reads {
		if c.matches(r) {
			c.done = true
			l.reads = append(l.reads[:i], l.reads[i+1:]...)
			return
		}
	}
	l.cars = append(l.cars, c)
	time.AfterFunc(time.Until(start.Add(after+u.settle)), func() { u.decide(c) })
}

// Read takes an LPR camera's read at a lane, by its capture time.
func (u *UnreadCars) Read(gate string, capture time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	l := u.lane(gate)
	for i, c := range l.cars {
		if c.matches(capture) {
			c.done = true
			l.cars = append(l.cars[:i], l.cars[i+1:]...)
			return
		}
	}
	l.reads = append(l.reads, capture)
	cutoff := time.Now().Add(-unreadReadKeep)
	drop := 0
	for drop < len(l.reads) && l.reads[drop].Before(cutoff) {
		drop++
	}
	l.reads = append(l.reads[:0:0], l.reads[drop:]...)
}

// decide reports a car whose window has passed without a read.
func (u *UnreadCars) decide(c *unreadCar) {
	u.mu.Lock()
	if c.done {
		u.mu.Unlock()
		return
	}
	c.done = true
	l := u.lane(c.gate)
	for i, w := range l.cars {
		if w == c {
			l.cars = append(l.cars[:i], l.cars[i+1:]...)
			break
		}
	}
	u.mu.Unlock()

	ts := func(t time.Time) string { return t.Local().Format(journalTimeFormat) }
	if u.journal != nil {
		u.journal.Write("car_unread", map[string]interface{}{
			"camera_ip": c.camera, "gate": c.gate, "direction": "entry",
			"approach_start": ts(c.approach), "beyond_start": ts(c.start), "beyond_end": ts(c.end),
		})
	}
	log.Printf("[COUNT] %s: a car came in at %s with no plate read", c.gate, c.start.Local().Format("15:04:05"))
	if u.send != nil {
		u.send(*c)
	}
}
