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
	// sends a car that backed out to the vehicle log, with the moment for its photo; nil: journal only
	sendBacked func(camera, gate string, e countEvent, photoAt time.Time)
	settle     time.Duration
}

// unreadCar is one counted car, waiting for its read.
type unreadCar struct {
	camera, gate         string
	approach, start, end time.Time // "approach" filled, "beyond" filled and emptied
	after                time.Duration
	done                 bool   // read, or reported as unread
	note                 string // for its card, such as "Arm already up 2 min 56 s" (ArmAlerts.CarCrossed)
	report               bool   // reported when no read comes (plate=); false: only matched, for its read's photo (photo=)
	notePhoto            []byte // goes with the note
}

type unreadLane struct {
	cars   []*unreadCar // waiting for a read, oldest first
	reads  []time.Time  // capture times of reads no car has claimed yet, oldest first
	latest *unreadCar   // the last car counted, for a second count of the same car
	all    []time.Time  // capture times of every read, claimed or not, for the last unreadReadKeep
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

// SetSendBacked sets how a car that backed out is sent to the vehicle log. Call before cars arrive.
func (u *UnreadCars) SetSendBacked(send func(camera, gate string, e countEvent, photoAt time.Time)) {
	u.sendBacked = send
}

// Backed takes a car the counter says backed out of a lane (count.go's car_backed: from
// "approach" filling, e.approach, to it emptying, e.end). It is sent only if the lane read no
// plate from unreadPlateBefore before it to after seconds after it: on 2026-10-22 six of seven
// daytime back-outs at South Enter were cars that drove in (white cars, "beyond" never
// filled), each read 1 to 4 s after "approach" emptied. Those are journaled as car_missed.
func (u *UnreadCars) Backed(camera, gate string, e countEvent, after time.Duration, photoAt time.Time) {
	from, to := e.approach.Add(-unreadPlateBefore), e.end.Add(after)
	time.AfterFunc(time.Until(to.Add(u.settle)), func() {
		u.mu.Lock()
		var read time.Time
		for _, r := range u.lane(gate).all {
			if !r.Before(from) && !r.After(to) {
				read = r
				break
			}
		}
		u.mu.Unlock()
		ts := func(t time.Time) string { return t.Local().Format(journalTimeFormat) }
		if !read.IsZero() {
			if u.journal != nil {
				u.journal.Write("car_missed", map[string]interface{}{
					"camera_ip": camera, "gate": gate, "direction": "entry",
					"approach_start": ts(e.approach), "approach_end": ts(e.end), "read_capture": ts(read),
				})
			}
			return
		}
		if u.sendBacked != nil {
			u.sendBacked(camera, gate, e, photoAt)
		}
	})
}

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

// Car takes a car counted into a lane by one of its cameras whose "beyond" zone has plate=,
// with the note for its card, if any. The note goes with its read (Read), or with the car
// when it has none; a read that arrived before the car was counted is already sent without it.
func (u *UnreadCars) Car(camera, gate string, approach, start, end time.Time, after time.Duration, note string, notePhoto []byte, report bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	l := u.lane(gate)
	if p := l.latest; p != nil && p.camera == camera && !start.Before(p.start) && start.Sub(p.end) <= unreadFragmentGap {
		if end.After(p.end) {
			p.end = end
		}
		return
	}
	c := &unreadCar{camera: camera, gate: gate, approach: approach, start: start, end: end, after: after, note: note, notePhoto: notePhoto, report: report}
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

// readCar is what a read learns from the counted car it is matched with.
type readCar struct {
	note      string    // for its card, such as "Arm already up 2 min 56 s"; "" for none
	notePhoto []byte    // goes with the note
	camera    string    // the camera that counted it; "" when the read matched no car
	start     time.Time // when it entered "beyond", for its overview photo
}

// Read takes an LPR camera's read at a lane, by its capture time, and returns the counted car
// it belongs to: its note and the note's photo, and when and by which camera it was seen
// entering "beyond". Empty when no counted car is waiting for it.
func (u *UnreadCars) Read(gate string, capture time.Time) readCar {
	u.mu.Lock()
	defer u.mu.Unlock()
	l := u.lane(gate)
	l.all = append(l.all, capture)
	keep := time.Now().Add(-unreadReadKeep)
	for len(l.all) > 0 && l.all[0].Before(keep) {
		l.all = l.all[1:]
	}
	for i, c := range l.cars {
		if c.matches(capture) {
			c.done = true
			l.cars = append(l.cars[:i], l.cars[i+1:]...)
			return readCar{note: c.note, notePhoto: c.notePhoto, camera: c.camera, start: c.start}
		}
	}
	l.reads = append(l.reads, capture)
	cutoff := time.Now().Add(-unreadReadKeep)
	drop := 0
	for drop < len(l.reads) && l.reads[drop].Before(cutoff) {
		drop++
	}
	l.reads = append(l.reads[:0:0], l.reads[drop:]...)
	return readCar{}
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
	if !c.report {
		return // matched only for a read's photo (photo=)
	}

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
