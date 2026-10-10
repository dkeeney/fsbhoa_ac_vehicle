package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
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
	Date    string // the record's own date and time: whole minutes, and the panel's clock
	Time    string
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

// Fields returns the record as journal fields, leaving out empty columns.
func (r RAMRecord) Fields() map[string]interface{} {
	f := map[string]interface{}{"raw": r.Raw, "record_kind": r.Kind}
	for k, v := range map[string]string{
		"account": r.Account, "ram_date": r.Date, "ram_time": r.Time, "type": r.Type,
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
	sources Sources
	journal *Journal
	debugf  func(string, ...interface{})
	active  atomic.Int32 // open RAM connections, for the health report
}

func (l *RAMListener) Run() {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", l.port))
	if err != nil {
		log.Printf("[ERROR] DoorKing listener could not start on port %d: %v", l.port, err)
		return
	}
	if len(l.sources) == 0 {
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
	if !l.sources.Allowed(remote) {
		log.Printf("[WARN] Refused DoorKing connection from %s: not a configured RAM stream source", remote)
		l.journal.Write("ram_refused", map[string]interface{}{"remote": ip.String()})
		return
	}

	l.active.Add(1)
	defer l.active.Add(-1)
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

	scanner := bufio.NewScanner(conn) // splits on LF and drops the CR
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		at := time.Now()
		rec := ParseRAMRecord(line)
		write, summary := filter.Add(line, rec.Kind == "incomplete", at)
		writeSummary(summary)
		if !write {
			continue
		}
		fields := rec.Fields()
		fields["remote"] = ip.String()
		l.journal.WriteAt(at, "ram_record", fields)
		if rec.Kind == "credential" {
			log.Printf("[RAM] %s %s %s %s %s", rec.Account, rec.Type, rec.Code, rec.Result, rec.Relay)
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
