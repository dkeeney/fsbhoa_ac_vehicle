package main

import (
	"encoding/json"
	"log"
	"math"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// FrigateEvents journals the gates' Frigate's tracked objects (docs/frigate_south_enter.md), to
// compare with the car count and the plate reads before anything uses them. It subscribes to
// <topic>/events on the gates' own broker on the alarm box and never publishes. Each object is
// journaled when it appears ("new"), each time it enters a zone ("zone"), and when it's gone
// ("end"), as `frigate_event`; Frigate's other updates (every few frames) are left out.
type FrigateEvents struct {
	broker, topic string
	journal       *Journal
}

const (
	frigateClientID = "fsbhoa_vehicle_" // + hostname: one subscriber per server
	frigateRetry    = 30 * time.Second
)

func NewFrigateEvents(broker, topic string, journal *Journal) *FrigateEvents {
	if topic == "" {
		topic = "frigate_gates"
	}
	return &FrigateEvents{broker: broker, topic: strings.TrimSuffix(topic, "/"), journal: journal}
}

// frigateObject is the part of Frigate's event "before" and "after" objects that is journaled.
type frigateObject struct {
	ID           string   `json:"id"`
	Camera       string   `json:"camera"`
	Label        string   `json:"label"`
	Score        float64  `json:"score"`
	TopScore     float64  `json:"top_score"`
	StartTime    float64  `json:"start_time"`
	EndTime      *float64 `json:"end_time"`
	CurrentZones []string `json:"current_zones"`
	EnteredZones []string `json:"entered_zones"`
	Stationary   bool     `json:"stationary"`
	FalsePos     bool     `json:"false_positive"`
}

type frigateMessage struct {
	Type   string        `json:"type"`
	Before frigateObject `json:"before"`
	After  frigateObject `json:"after"`
}

// Run connects and stays subscribed; the client reconnects by itself after a broken connection.
func (f *FrigateEvents) Run() {
	opts := mqtt.NewClientOptions().
		AddBroker("tcp://" + f.broker).
		SetClientID(frigateClientID + hostname).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(frigateRetry).
		SetOnConnectHandler(func(c mqtt.Client) {
			topic := f.topic + "/events"
			if t := c.Subscribe(topic, 0, f.handle); t.Wait() && t.Error() != nil {
				log.Printf("[FRIGATE] Subscribe to %s failed: %v", topic, t.Error())
				return
			}
			log.Printf("[FRIGATE] Subscribed to %s on %s", topic, f.broker)
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("[FRIGATE] Lost the connection to %s: %v", f.broker, err)
		})
	mqtt.NewClient(opts).Connect() // with SetConnectRetry it keeps trying in the background
}

func (f *FrigateEvents) handle(_ mqtt.Client, m mqtt.Message) {
	var msg frigateMessage
	if err := json.Unmarshal(m.Payload(), &msg); err != nil {
		log.Printf("[FRIGATE] Bad event: %v", err)
		return
	}
	if entry := frigateEntry(msg); entry != nil {
		f.journal.Write("frigate_event", entry)
	}
}

// frigateEntry is the journal entry for one event, or nil when it says nothing new: a "new"
// object, a zone it entered, or its "end".
func frigateEntry(msg frigateMessage) map[string]interface{} {
	o := msg.After
	var event, zone string
	switch msg.Type {
	case "new":
		event = "new"
	case "end":
		event = "end"
	case "update":
		before := map[string]bool{}
		for _, z := range msg.Before.EnteredZones {
			before[z] = true
		}
		for _, z := range o.EnteredZones {
			if !before[z] {
				event, zone = "zone", z
			}
		}
	}
	if event == "" {
		return nil
	}
	at := func(t float64) string {
		sec, frac := math.Modf(t)
		return time.Unix(int64(sec), int64(frac*1e9)).Local().Format(journalTimeFormat)
	}
	entry := map[string]interface{}{
		"event": event, "id": o.ID, "camera": o.Camera, "label": o.Label,
		"score": math.Round(o.Score*100) / 100, "top_score": math.Round(o.TopScore*100) / 100,
		"start": at(o.StartTime), "current_zones": o.CurrentZones, "entered_zones": o.EnteredZones,
	}
	if zone != "" {
		entry["zone"] = zone
	}
	if o.EndTime != nil {
		entry["end"] = at(*o.EndTime)
	}
	if o.Stationary {
		entry["stationary"] = true
	}
	if o.FalsePos {
		entry["false_positive"] = true
	}
	return entry
}
