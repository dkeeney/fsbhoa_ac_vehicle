package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// DoorKingEvents sends each DoorKing authentication to the vehicle log on its own, with no
// loop or plate, when the "doorking_events" setting is on. It is a stopgap until per-lane
// correlation (CLAUDE.md, "Build order", step 3) exists, and is off by default.
//
// Repeat reads of the same code at the same lane are one presentation, so a tag sitting in
// front of the reader (one read a second) gives one event. A read joins the presentation if
// it comes within dkRepeatGap of the previous read of that code.
type DoorKingEvents struct {
	enabled    atomic.Bool
	dispatcher *Dispatcher

	mu   sync.Mutex
	last map[string]time.Time // lane, type and code -> last read
}

const dkRepeatGap = 10 * time.Second

// dkAuthTypes maps RAM's record types to the vehicle log's auth_type.
var dkAuthTypes = map[string]string{
	"Cards":      "DK_WINDSHIELD",
	"Entry Code": "DK_ENTRY_CODE",
}

func NewDoorKingEvents(dispatcher *Dispatcher, enabled bool) *DoorKingEvents {
	d := &DoorKingEvents{dispatcher: dispatcher, last: map[string]time.Time{}}
	d.SetEnabled(enabled)
	return d
}

func (d *DoorKingEvents) SetEnabled(on bool) {
	if d.enabled.Swap(on) != on {
		log.Printf("[CONFIG] DoorKing-only vehicle events: %v", on)
	}
}

// Handle takes one streamed record that arrived at the given time. lane is its entrance
// lane's name, or "" if its RAM account belongs to no lane. It reports whether an event
// was queued.
func (d *DoorKingEvents) Handle(rec RAMRecord, lane string, at time.Time) bool {
	authType, ok := dkAuthTypes[rec.Type]
	if !d.enabled.Load() || !ok || rec.Kind != "credential" || rec.Result != "Admit" || rec.Code == "" {
		return false
	}
	gate := lane
	if gate == "" {
		gate = rec.Account // a RAM account with no lane still shows where it came from
	}

	key := gate + "\x00" + authType + "\x00" + rec.Code
	d.mu.Lock()
	prev, seen := d.last[key]
	d.last[key] = at
	for k, t := range d.last { // forget presentations that are over
		if at.Sub(t) > dkRepeatGap {
			delete(d.last, k)
		}
	}
	d.mu.Unlock()
	if seen && at.Sub(prev) <= dkRepeatGap {
		return false // the same presentation
	}

	uid := fmt.Sprintf("dk_%d", at.UnixNano())
	details := map[string]interface{}{
		"source":      "doorking_only", // not correlated with a loop or a plate
		"account":     rec.Account,
		"relay":       rec.Relay,
		"source_host": hostname,
	}
	for k, v := range map[string]string{"ram_date": rec.Date, "ram_time": rec.Time, "ram_timestamp": rec.Stamp} {
		if v != "" {
			details[k] = v
		}
	}
	payload, err := json.Marshal(map[string]interface{}{
		"event_uid":       uid,
		"event_timestamp": at.Local().Format("2006-01-02 15:04:05.000"),
		"gate_identifier": gate,
		"auth_id":         rec.Code,
		"auth_type":       authType,
		"raw_details":     details,
	})
	if err != nil {
		log.Printf("[ERROR] DoorKing event JSON: %v", err)
		return false
	}
	if err := d.dispatcher.Enqueue(uid, payload); err != nil {
		log.Printf("[ERROR] Could not queue DoorKing event %s: %v", uid, err)
		return false
	}
	log.Printf("[DISPATCH] DoorKing-only event %s: %s %s %s", uid, gate, authType, rec.Code)
	return true
}
