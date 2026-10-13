package main

import (
	"log"
	"net/netip"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// DoorKing RAM "Live Streaming" records. RAM writes them to its Disk File (ramfile.go) as
// fixed-width lines framed by CR LF, or in an XML format. The format is in CLAUDE.md, "RAM
// Live Streaming format". (Until 2026-10-13 RAM also streamed them to a TCP listener here;
// RAM never reconnected a dropped stream, so the file replaced it.)

// RAMRecord is one streamed record, split into its fixed columns.
type RAMRecord struct {
	Raw     string
	Kind    string // "credential", "status", "incomplete" or "other"
	Account string
	Format  string // "text" or "xml" (RAM's two Live Streaming formats)
	Date    string // the record's own date and time: whole minutes, and the panel's clock
	Time    string
	Stamp   string // XML format: the record's own timestamp, as sent (its UTC offset ignores DST)
	Type    string // "Cards", "Entry Code", "Network", "RS232", ...
	Code    string // device number or gate code, exactly as sent
	Name    string
	Result  string // "Admit"
	Relay   string
	Message string // status records only ("Connection", "Download Data", ...)
}

// col returns s[from:to] with spaces trimmed, tolerating short lines; to < 0 means to the end.
func col(s string, from, to int) string {
	if from >= len(s) {
		return ""
	}
	if to < 0 || to > len(s) {
		to = len(s)
	}
	return strings.TrimSpace(s[from:to])
}

// ParseRAMRecord splits one record (without its CR LF) into columns.
func ParseRAMRecord(line string) RAMRecord {
	r := RAMRecord{
		Raw:     line,
		Format:  "text",
		Account: col(line, 0, 23),
		Date:    col(line, 23, 35),
		Time:    col(line, 35, 44),
		Type:    col(line, 44, 57),
	}
	switch r.Type {
	case "Network", "RS232":
		r.Kind = "status"
		r.Message = col(line, 65, -1)
		return r
	case "Cards", "Entry Code":
		r.Kind = "credential"
	case "":
		r.Kind = "incomplete"
	default:
		r.Kind = "other"
	}
	r.Code = col(line, 57, 65)
	r.Name = col(line, 65, 83)
	r.Result = col(line, 83, 91)
	r.Relay = col(line, 91, -1)
	return r
}

// xmlTag matches one line of an XML-format record, such as <Number>06549</Number>. RAM
// doesn't escape its values (a name came through as "<Name>& SHUT, OPEN</Name>"), so this
// is matched line by line instead of with an XML parser.
var xmlTag = regexp.MustCompile(`^<(\w+)>(.*)</(\w+)>$`)

// ParseRAMXML builds a record from the lines of one XML-format record, from <Transaction>
// to </Transaction>. The fields are those of the text format, under other names.
func ParseRAMXML(lines []string) RAMRecord {
	tags := map[string]string{}
	for _, l := range lines {
		if m := xmlTag.FindStringSubmatch(strings.TrimSpace(l)); m != nil && m[1] == m[3] {
			tags[m[1]] = strings.TrimSpace(m[2])
		}
	}
	r := RAMRecord{
		Raw:     strings.Join(lines, ""),
		Format:  "xml",
		Account: tags["Account"],
		Stamp:   tags["Timestamp"],
		Type:    tags["Event"],
	}
	switch r.Type {
	case "Network", "RS232":
		r.Kind = "status"
		r.Message = tags["Name"]
		return r
	case "Cards", "Entry Code":
		r.Kind = "credential"
	case "":
		r.Kind = "incomplete"
	default:
		r.Kind = "other"
	}
	r.Code = tags["Number"]
	r.Name = tags["Name"]
	r.Result = tags["Access"]
	r.Relay = tags["Relay"]
	return r
}

// Fields returns the record as journal fields, leaving out empty columns.
func (r RAMRecord) Fields() map[string]interface{} {
	f := map[string]interface{}{"raw": r.Raw, "record_kind": r.Kind, "format": r.Format}
	for k, v := range map[string]string{
		"account": r.Account, "ram_date": r.Date, "ram_time": r.Time, "ram_timestamp": r.Stamp, "type": r.Type,
		"code": r.Code, "name": r.Name, "result": r.Result, "relay": r.Relay, "message": r.Message,
	} {
		if v != "" {
			f[k] = v
		}
	}
	return f
}

// repeatFilter collapses floods of identical records (seen: 28,432 copies of one incomplete
// record in seven minutes) without hiding real repeat reads, which come about a second
// apart. An identical record is collapsed when it arrives within repeatWindow of the last
// copy, or at any gap when it is incomplete (a real read always has a type).
type repeatFilter struct {
	last    string
	firstAt time.Time // first collapsed copy
	lastAt  time.Time // newest copy, written or collapsed
	count   int       // copies collapsed since the last one written
}

const repeatWindow = 250 * time.Millisecond

type repeatSummary struct {
	raw         string
	count       int
	first, last time.Time
}

// Add reports whether the record should be written, and returns a summary of the copies
// collapsed before it, if any.
func (f *repeatFilter) Add(raw string, incomplete bool, at time.Time) (bool, *repeatSummary) {
	if raw == f.last && (incomplete || at.Sub(f.lastAt) <= repeatWindow) {
		if f.count == 0 {
			f.firstAt = at
		}
		f.count++
		f.lastAt = at
		return false, nil
	}
	summary := f.Flush()
	f.last, f.lastAt = raw, at
	return true, summary
}

// Flush returns the summary of copies collapsed so far, if any, and resets the count.
func (f *repeatFilter) Flush() *repeatSummary {
	if f.count == 0 {
		return nil
	}
	s := &repeatSummary{raw: f.last, count: f.count, first: f.firstAt, last: f.lastAt}
	f.count = 0
	return s
}

// Sources is an allowlist of remote addresses. This machine (loopback) is always allowed.
type Sources map[netip.Addr]bool

func NewSources(list []string) Sources {
	s := Sources{}
	for _, item := range list {
		if addr, err := netip.ParseAddr(strings.TrimSpace(item)); err == nil {
			s[addr.Unmap()] = true
		} else if strings.TrimSpace(item) != "" {
			log.Printf("[CONFIG] WARNING: ignoring %q in an address list: not an IP address", item)
		}
	}
	return s
}

// remoteAddr returns the IP part of a "host:port" remote address.
func remoteAddr(hostport string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(hostport)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

func (s Sources) Allowed(hostport string) bool {
	addr, ok := remoteAddr(hostport)
	return ok && (addr.IsLoopback() || s[addr])
}

// DoorKingRecords journals RAM's records, tagged with their entrance lane, and hands
// authentications to the DoorKing-only events.
type DoorKingRecords struct {
	gates   atomic.Pointer[Gates] // RAM account -> entrance lane; replaced when the config changes
	events  *DoorKingEvents       // optional: authentications sent as events on their own
	journal *Journal
	debugf  func(string, ...interface{})
}

func NewDoorKingRecords(journal *Journal, debugf func(string, ...interface{})) *DoorKingRecords {
	d := &DoorKingRecords{journal: journal, debugf: debugf}
	d.SetGates(NewGates(nil, nil))
	return d
}

func (d *DoorKingRecords) SetGates(g *Gates) { d.gates.Store(g) }

// SetEvents sets where authentications go as events of their own. Call before reading.
func (d *DoorKingRecords) SetEvents(e *DoorKingEvents) { d.events = e }

// ramLines turns lines of RAM's Live Streaming output into records. It assembles XML-format
// records, collapses floods of identical records, journals each record tagged with its lane,
// and hands authentications to the DoorKing-only events.
type ramLines struct {
	d      *DoorKingRecords
	tag    map[string]interface{} // added to each journal entry, such as "source": "file"
	from   string                 // where the lines come from, for the log
	filter repeatFilter
	xml    []string // lines of an XML record in progress
}

// Line takes one line (its CR LF optional) that arrived at the given time, and returns the
// record it completed, if any.
func (p *ramLines) Line(line string, at time.Time) (RAMRecord, bool) {
	line = strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return RAMRecord{}, false
	}
	// RAM writes each record in the text format on one line, or in XML over several lines,
	// from <Transaction> to </Transaction>. Both are accepted, so switching the format in
	// RAM, or someone setting it to XML, can't lose a record.
	var rec RAMRecord
	if trimmed == "<Transaction>" || p.xml != nil {
		if trimmed == "<Transaction>" {
			p.xml = nil // a new record; anything unfinished before it is dropped
		}
		p.xml = append(p.xml, trimmed)
		if trimmed != "</Transaction>" && len(p.xml) < 50 {
			return RAMRecord{}, false
		}
		rec = ParseRAMXML(p.xml)
		line = rec.Raw
		p.xml = nil
	} else if strings.HasPrefix(trimmed, "<") {
		// Part of an XML record whose start we missed (seen right after a reconnect)
		rec = RAMRecord{Raw: line, Format: "xml", Kind: "other"}
	} else {
		rec = ParseRAMRecord(line)
	}
	p.write(rec, line, at)
	return rec, true
}

func (p *ramLines) write(rec RAMRecord, line string, at time.Time) {
	write, summary := p.filter.Add(line, rec.Kind == "incomplete", at)
	p.summary(summary)
	if !write {
		return
	}
	fields := rec.Fields()
	for k, v := range p.tag {
		fields[k] = v
	}
	gate := p.d.gates.Load().ForAccount(rec.Account)
	if gate != "" {
		fields["gate"] = gate
	}
	p.d.journal.WriteAt(at, "ram_record", fields)
	if p.d.events != nil {
		p.d.events.Handle(rec, gate, at)
	}
	if rec.Kind == "credential" {
		if gate == "" {
			gate = "no gate"
		}
		log.Printf("[RAM] %s (%s) %s %s %s %s", rec.Account, gate, rec.Type, rec.Code, rec.Result, rec.Relay)
	} else {
		p.d.debugf("[RAM] %s record: %q", rec.Kind, strings.TrimSpace(line))
	}
}

// Flush journals the copies collapsed so far. Call it when a batch of lines ends.
func (p *ramLines) Flush() {
	p.summary(p.filter.Flush())
}

func (p *ramLines) summary(s *repeatSummary) {
	if s == nil {
		return
	}
	fields := map[string]interface{}{"raw": s.raw, "count": s.count, "first": s.first.Local().Format(journalTimeFormat)}
	for k, v := range p.tag {
		fields[k] = v
	}
	p.d.journal.WriteAt(s.last, "ram_repeat", fields)
	log.Printf("[RAM] %d identical copies of one record collapsed from %s", s.count, p.from)
}
