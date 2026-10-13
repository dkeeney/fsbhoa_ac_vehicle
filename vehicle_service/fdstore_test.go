package main

import (
	"strings"
	"testing"
)

func TestHeldStreamNameRoundTrip(t *testing.T) {
	h := heldStream{ID: 1760371200123456789, Since: 1760371200123, Accounts: []string{"SOUTH GATE NEW", "WORKBENCH"}}
	name := h.Name()
	if !strings.HasPrefix(name, heldStreamPrefix) || len(name) > fdNameMax || strings.ContainsAny(name, ": \n") {
		t.Fatalf("name %q isn't a valid store name", name)
	}
	got, ok := parseHeldStream(name)
	if !ok || got.ID != h.ID || got.Since != h.Since || strings.Join(got.Accounts, ",") != "SOUTH GATE NEW,WORKBENCH" {
		t.Fatalf("parseHeldStream(%q) = %+v, %v", name, got, ok)
	}
}

func TestHeldStreamNameTooLong(t *testing.T) {
	h := heldStream{ID: 1, Since: 2, Accounts: []string{strings.Repeat("A", 300)}}
	name := h.Name()
	if len(name) > fdNameMax {
		t.Fatalf("name is %d characters, over systemd's %d", len(name), fdNameMax)
	}
	got, ok := parseHeldStream(name)
	if !ok || got.ID != 1 || got.Since != 2 || len(got.Accounts) != 0 {
		t.Fatalf("parseHeldStream = %+v, %v; want the stream without its accounts", got, ok)
	}
}

func TestParseHeldStreamRejectsOthers(t *testing.T) {
	for _, name := range []string{"ram-listener", "ram-conn.!!!", "unknown"} {
		if _, ok := parseHeldStream(name); ok {
			t.Errorf("parseHeldStream(%q) accepted it", name)
		}
	}
}
