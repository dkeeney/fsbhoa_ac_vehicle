package main

import (
	"encoding/json"
	"testing"
)

func TestFrigateEntry(t *testing.T) {
	parse := func(s string) frigateMessage {
		var m frigateMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	newObj := parse(`{"type":"new","before":{},"after":{"id":"1761160000.1-abc","camera":"south_enter","label":"car","score":0.71,"top_score":0.71,"start_time":1761160000.1,"end_time":null,"current_zones":[],"entered_zones":[]}}`)
	if e := frigateEntry(newObj); e == nil || e["event"] != "new" || e["label"] != "car" {
		t.Fatalf("new: %v", e)
	}
	same := parse(`{"type":"update","before":{"entered_zones":["approach"]},"after":{"id":"x","entered_zones":["approach"],"current_zones":["approach"]}}`)
	if e := frigateEntry(same); e != nil {
		t.Fatalf("an update with no new zone was journaled: %v", e)
	}
	zone := parse(`{"type":"update","before":{"entered_zones":["approach"]},"after":{"id":"x","entered_zones":["approach","beyond"],"current_zones":["beyond"]}}`)
	if e := frigateEntry(zone); e == nil || e["event"] != "zone" || e["zone"] != "beyond" {
		t.Fatalf("zone: %v", e)
	}
	end := parse(`{"type":"end","before":{},"after":{"id":"x","label":"truck","start_time":1761160000.1,"end_time":1761160012.6,"entered_zones":["approach","beyond"]}}`)
	if e := frigateEntry(end); e == nil || e["event"] != "end" || e["end"] == nil {
		t.Fatalf("end: %v", e)
	}
}
