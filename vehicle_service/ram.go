package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DoorKing RAM "Live Streaming" receiver. RAM connects to us over TCP, keeps the connection
// open for its session and sends fixed-width records framed by CR LF. The format is in
// CLAUDE.md, "RAM Live Streaming format".

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

// RAMListener accepts RAM Live Streaming connections and journals every record.
type RAMListener struct {
	port    int
	sources atomic.Pointer[Sources] // replaced when the config file changes
	gates   atomic.Pointer[Gates]   // RAM account -> entrance lane, for tagging records
	events  *DoorKingEvents         // optional: authentications sent as events on their own
	journal *Journal
	debugf  func(string, ...interface{})

	mu      sync.Mutex
	streams map[int]*ramStream // open connections, for the health report
	nextID  int
}

// ramStream describes one open RAM connection.
type ramStream struct {
	remote      string
	connectedAt time.Time
	lastRecord  time.Time
	accounts    map[string]bool
}

func NewRAMListener(port int, sources Sources, journal *Journal, debugf func(string, ...interface{})) *RAMListener {
	l := &RAMListener{port: port, journal: journal, debugf: debugf, streams: map[int]*ramStream{}}
	l.SetSources(sources)
	l.SetGates(NewGates(nil, nil))
	return l
}

func (l *RAMListener) SetSources(s Sources) { l.sources.Store(&s) }

func (l *RAMListener) SetGates(g *Gates) { l.gates.Store(g) }

// SetEvents sets where authentications go as events of their own. Call before Run.
func (l *RAMListener) SetEvents(e *DoorKingEvents) { l.events = e }

// Allowed reports whether a remote address is a configured RAM stream source.
func (l *RAMListener) Allowed(hostport string) bool { return l.sources.Load().Allowed(hostport) }

// Accounts returns each RAM account seen on an open connection, with when the newest of
// those connections opened. RAM sends a "Network Connection" record for the account as
// soon as it connects, so an account appears here at once.
func (l *RAMListener) Accounts() map[string]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]time.Time{}
	for _, st := range l.streams {
		for a := range st.accounts {
			if st.connectedAt.After(out[a]) {
				out[a] = st.connectedAt
			}
		}
	}
	return out
}

// Streams describes the open RAM connections, for the health report.
func (l *RAMListener) Streams() []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []map[string]interface{}{}
	for _, st := range l.streams {
		accounts := []string{}
		for a := range st.accounts {
			accounts = append(accounts, a)
		}
		sort.Strings(accounts)
		entry := map[string]interface{}{
			"remote":       st.remote,
			"connected_at": st.connectedAt.Local().Format(journalTimeFormat),
			"accounts":     accounts,
		}
		if !st.lastRecord.IsZero() {
			entry["last_record_at"] = st.lastRecord.Local().Format(journalTimeFormat)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["connected_at"].(string) < out[j]["connected_at"].(string) })
	return out
}

func (l *RAMListener) Run() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", l.port))
	if err != nil {
		log.Printf("[ERROR] DoorKing listener could not start on port %d: %v", l.port, err)
		return
	}
	if len(*l.sources.Load()) == 0 {
		log.Printf("[CONFIG] WARNING: no RAM stream sources configured; only this machine may connect to port %d", l.port)
	}
	log.Printf("DoorKing RAM listener on port %d", l.port)
	l.serve(ln)
}

func (l *RAMListener) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			log.Printf("[ERROR] DoorKing listener accept: %v", err)
			time.Sleep(time.Second)
			continue
		}
		go l.handle(conn)
	}
}

func (l *RAMListener) handle(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	ip, _ := remoteAddr(remote)
	if !l.sources.Load().Allowed(remote) {
		log.Printf("[WARN] Refused DoorKing connection from %s: not a configured RAM stream source", remote)
		l.journal.Write("ram_refused", map[string]interface{}{"remote": ip.String()})
		return
	}

	stream := &ramStream{remote: ip.String(), connectedAt: time.Now(), accounts: map[string]bool{}}
	l.mu.Lock()
	id := l.nextID
	l.nextID++
	l.streams[id] = stream
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.streams, id)
		l.mu.Unlock()
	}()
	log.Printf("[RAM] Connected: %s", remote)
	l.journal.Write("ram_connect", map[string]interface{}{"remote": ip.String()})

	var filter repeatFilter
	writeSummary := func(s *repeatSummary) {
		if s == nil {
			return
		}
		l.journal.WriteAt(s.last, "ram_repeat", map[string]interface{}{
			"remote": ip.String(), "raw": s.raw, "count": s.count,
			"first": s.first.Local().Format(journalTimeFormat),
		})
		log.Printf("[RAM] %d identical copies of one record collapsed from %s", s.count, remote)
	}

	// RAM sends each record in the text format on one line, or in XML over several lines,
	// from <Transaction> to </Transaction>. Both are accepted, so switching the format in
	// RAM, or someone setting it to XML, can't lose a record.
	var xml []string // lines of an XML record in progress

	scanner := bufio.NewScanner(conn) // splits on LF and drops the CR
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var rec RAMRecord
		if trimmed == "<Transaction>" || xml != nil {
			if trimmed == "<Transaction>" {
				xml = nil // a new record; anything unfinished before it is dropped
			}
			xml = append(xml, trimmed)
			if trimmed != "</Transaction>" && len(xml) < 50 {
				continue
			}
			rec = ParseRAMXML(xml)
			line = rec.Raw
			xml = nil
		} else if strings.HasPrefix(trimmed, "<") {
			// Part of an XML record whose start we missed (seen right after a reconnect)
			rec = RAMRecord{Raw: line, Format: "xml", Kind: "other"}
		} else {
			rec = ParseRAMRecord(line)
		}
		at := time.Now()
		l.mu.Lock()
		stream.lastRecord = at
		if rec.Account != "" {
			stream.accounts[rec.Account] = true
		}
		l.mu.Unlock()
		write, summary := filter.Add(line, rec.Kind == "incomplete", at)
		writeSummary(summary)
		if !write {
			continue
		}
		fields := rec.Fields()
		fields["remote"] = ip.String()
		gate := l.gates.Load().ForAccount(rec.Account)
		if gate != "" {
			fields["gate"] = gate
		}
		l.journal.WriteAt(at, "ram_record", fields)
		if l.events != nil {
			l.events.Handle(rec, gate, at)
		}
		if rec.Kind == "credential" {
			if gate == "" {
				gate = "no gate"
			}
			log.Printf("[RAM] %s (%s) %s %s %s %s", rec.Account, gate, rec.Type, rec.Code, rec.Result, rec.Relay)
		} else {
			l.debugf("[RAM] %s record: %q", rec.Kind, strings.TrimSpace(line))
		}
	}
	writeSummary(filter.Flush())

	reason := "closed by RAM"
	if err := scanner.Err(); err != nil {
		reason = err.Error()
	}
	log.Printf("[RAM] Disconnected: %s (%s)", remote, reason)
	l.journal.Write("ram_disconnect", map[string]interface{}{"remote": ip.String(), "reason": reason})
}
