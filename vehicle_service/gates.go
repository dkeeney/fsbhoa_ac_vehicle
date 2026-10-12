package main

import (
	"log"
	"net/netip"
	"strings"
)

// GateConfig is one entrance lane from the vehicle settings' Entrance Lanes table (config
// key "gates"; CLAUDE.md, "Lanes and devices"). Each has its own DoorKing controller, loop
// sensor and cameras.
type GateConfig struct {
	Name       string `json:"name"`        // gate_identifier in the log and on the monitor
	RAMAccount string `json:"ram_account"` // the account name in RAM's stream records
	LoopIP     string `json:"loop_ip"`     // loop sensor (Shelly)
	LPRIP      string `json:"lpr_ip"`      // LPR camera
	OverviewIP string `json:"overview_ip"` // overview (context) camera
}

// ExitLaneConfig is one exit lane from the Exit Lanes table. Exit lanes have only cameras,
// and aren't tied to an entrance: a car may enter at any entrance and leave at any exit.
type ExitLaneConfig struct {
	Name       string `json:"name"` // may repeat an entrance's name (e.g. "South")
	LPRIP      string `json:"lpr_ip"`
	OverviewIP string `json:"overview_ip"` // optional
}

// GateDevice says which lane a device address belongs to, and which device it is.
type GateDevice struct {
	Gate      string // the lane's name
	Direction string // "entry" or "exit"
	Device    string // "loop", "lpr" or "overview"
}

// Gates indexes the lane tables for lookups by RAM account and by device address.
type Gates struct {
	byAccount map[string]string // RAM account, upper case -> gate name
	byAddr    map[netip.Addr]GateDevice
}

// NewGates builds the index. WordPress checks the tables when they are saved; anything
// that still doesn't fit (a bad address, a repeat) is logged and left out.
func NewGates(entrances []GateConfig, exits []ExitLaneConfig) *Gates {
	g := &Gates{byAccount: map[string]string{}, byAddr: map[netip.Addr]GateDevice{}}
	for _, gate := range entrances {
		name := strings.TrimSpace(gate.Name)
		if name == "" {
			continue
		}
		if acct := strings.ToUpper(strings.TrimSpace(gate.RAMAccount)); acct != "" {
			if other, dup := g.byAccount[acct]; dup {
				log.Printf("[CONFIG] WARNING: RAM account %q is set for both %s and %s; using %s", gate.RAMAccount, other, name, other)
			} else {
				g.byAccount[acct] = name
			}
		}
		g.addDevices(name, "entry", map[string]string{"loop": gate.LoopIP, "lpr": gate.LPRIP, "overview": gate.OverviewIP})
	}
	for _, lane := range exits {
		if name := strings.TrimSpace(lane.Name); name != "" {
			g.addDevices(name, "exit", map[string]string{"lpr": lane.LPRIP, "overview": lane.OverviewIP})
		}
	}
	return g
}

func (g *Gates) addDevices(lane, direction string, devices map[string]string) {
	for _, device := range []string{"loop", "lpr", "overview"} { // fixed order: the first use of an address wins
		ip := strings.TrimSpace(devices[device])
		if ip == "" {
			continue
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			log.Printf("[CONFIG] WARNING: ignoring %s %s's %s address %q: not an IP address", direction, lane, device, ip)
			continue
		}
		addr = addr.Unmap()
		if other, dup := g.byAddr[addr]; dup {
			log.Printf("[CONFIG] WARNING: %s is set for both %s %s %s and %s %s %s; using the first", addr, other.Direction, other.Gate, other.Device, direction, lane, device)
			continue
		}
		g.byAddr[addr] = GateDevice{Gate: lane, Direction: direction, Device: device}
	}
}

// ForAccount returns the gate whose RAM account sent a record, or "" if none.
func (g *Gates) ForAccount(account string) string {
	return g.byAccount[strings.ToUpper(strings.TrimSpace(account))]
}

// ForAddr returns the gate device at a "host:port" remote address.
func (g *Gates) ForAddr(hostport string) (GateDevice, bool) {
	addr, ok := remoteAddr(hostport)
	if !ok {
		return GateDevice{}, false
	}
	d, ok := g.byAddr[addr]
	return d, ok
}

// DeviceSources is the webhook allowlist: every lane's devices, plus any extra addresses.
func (g *Gates) DeviceSources(extra []string) Sources {
	s := NewSources(extra)
	for addr := range g.byAddr {
		s[addr] = true
	}
	return s
}

// Count returns how many lanes have a RAM account or a device.
func (g *Gates) Count() int {
	lanes := map[string]bool{}
	for _, n := range g.byAccount {
		lanes["entry "+n] = true
	}
	for _, d := range g.byAddr {
		lanes[d.Direction+" "+d.Gate] = true
	}
	return len(lanes)
}
