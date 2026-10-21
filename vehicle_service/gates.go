package main

import (
	"log"
	"net/netip"
	"strings"
	"time"
)

// GateConfig is one entrance lane from the vehicle settings' Entrance Lanes table (config
// key "gates"; CLAUDE.md, "Lanes and devices"). Each has its own DoorKing controller, loop
// sensor and cameras.
type GateConfig struct {
	Name       string   `json:"name"`                 // gate_identifier in the log and on the monitor
	RAMAccount string   `json:"ram_account"`          // the account name in RAM's stream records
	LoopIP     string   `json:"loop_ip"`              // loop sensor (Shelly)
	LPRIP      string   `json:"lpr_ip"`               // LPR camera
	OverviewIP string   `json:"overview_ip"`          // overview (context) camera: sees the vehicle pass the gate, and the arm
	KeypadIP   string   `json:"keypad_ip"`            // keypad camera: sees the vehicle at the keypad and tag reader
	ArmIP      string   `json:"arm_ip,omitempty"`     // arm camera, for the broken arm alert; "": the overview camera sees the arm
	PhotoLead  *float64 `json:"photo_lead,omitempty"` // seconds before the plate's capture for the overview photo; nil: overviewPhotoLead
	PhotoAuto  bool     `json:"photo_auto,omitempty"` // pick the overview photo by content instead (photo.go)
}

// ExitLaneConfig is one exit lane from the Exit Lanes table. Exit lanes have only cameras,
// and aren't tied to an entrance: a car may enter at any entrance and leave at any exit.
type ExitLaneConfig struct {
	Name       string   `json:"name"` // may repeat an entrance's name (e.g. "South")
	LPRIP      string   `json:"lpr_ip"`
	OverviewIP string   `json:"overview_ip"` // optional
	PhotoLead  *float64 `json:"photo_lead,omitempty"`
	PhotoAuto  bool     `json:"photo_auto,omitempty"`
}

// GateDevice says which lane a device address belongs to, and which device it is.
type GateDevice struct {
	Gate      string // the lane's name
	Direction string // "entry" or "exit"
	Device    string // "loop", "lpr", "overview", "keypad" or "arm"
}

// Gates indexes the lane tables for lookups by RAM account and by device address.
type Gates struct {
	byAccount map[string]string // RAM account, upper case -> gate name
	byAddr    map[netip.Addr]GateDevice
	leads     map[string]float64 // "<direction> <lane>" -> photo lead, where set
	autos     map[string]bool    // "<direction> <lane>" -> photo picked by content
}

// NewGates builds the index. WordPress checks the tables when they are saved; anything
// that still doesn't fit (a bad address, a repeat) is logged and left out.
func NewGates(entrances []GateConfig, exits []ExitLaneConfig) *Gates {
	g := &Gates{byAccount: map[string]string{}, byAddr: map[netip.Addr]GateDevice{}, leads: map[string]float64{}, autos: map[string]bool{}}
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
		g.addDevices(name, "entry", map[string]string{"loop": gate.LoopIP, "lpr": gate.LPRIP, "overview": gate.OverviewIP, "keypad": gate.KeypadIP, "arm": gate.ArmIP})
		if gate.PhotoLead != nil {
			g.leads["entry "+name] = *gate.PhotoLead
		}
		g.autos["entry "+name] = gate.PhotoAuto
	}
	for _, lane := range exits {
		if name := strings.TrimSpace(lane.Name); name != "" {
			g.addDevices(name, "exit", map[string]string{"lpr": lane.LPRIP, "overview": lane.OverviewIP})
			if lane.PhotoLead != nil {
				g.leads["exit "+name] = *lane.PhotoLead
			}
			g.autos["exit "+name] = lane.PhotoAuto
		}
	}
	return g
}

func (g *Gates) addDevices(lane, direction string, devices map[string]string) {
	for _, device := range []string{"loop", "lpr", "overview", "keypad", "arm"} { // fixed order: the first use of an address wins
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

// ForIP returns the gate device at an IP address, such as a camera's from its uploads.
func (g *Gates) ForIP(ip string) (GateDevice, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return GateDevice{}, false
	}
	d, ok := g.byAddr[addr.Unmap()]
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

// OverviewLane is a lane's overview camera, and how long before a plate's capture its photo
// of the car is taken, or whether the photo is picked by content (Auto).
type OverviewLane struct {
	IP   string
	Lead time.Duration
	Auto bool
}

// OverviewCameras returns each lane's overview camera, keyed by "<direction> <lane>", with
// the lane's photo lead (overviewPhotoLead where the lane has none).
// ArmCameras is the address of each entrance lane's arm camera (blank in a lane whose overview
// camera sees the arm). Their frames are kept too, for counting cars at the arm (count.go).
func (g *Gates) ArmCameras() []string {
	var ips []string
	for addr, d := range g.byAddr {
		if d.Device == "arm" {
			ips = append(ips, addr.String())
		}
	}
	return ips
}

func (g *Gates) OverviewCameras() map[string]OverviewLane {
	cams := map[string]OverviewLane{}
	for addr, d := range g.byAddr {
		if d.Device == "overview" {
			key := d.Direction + " " + d.Gate
			lead := overviewPhotoLead
			if s, ok := g.leads[key]; ok {
				lead = time.Duration(s * float64(time.Second))
			}
			cams[key] = OverviewLane{IP: addr.String(), Lead: lead, Auto: g.autos[key]}
		}
	}
	return cams
}
