package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Holding RAM's connections across restarts (CLAUDE.md, "RAM connection hold"). RAM opens a
// dropped stream again only when it next starts a job, so a restart of this service would
// cut the stream. systemd's file descriptor store keeps sockets open for a service while it
// restarts: the service hands them to systemd (sd_notify "FDSTORE=1"), and systemd passes
// them to the next start (LISTEN_FDS, LISTEN_FDNAMES). The RAM listener and each open RAM
// connection are kept there, so RAM never sees the restart. The unit needs NotifyAccess=main
// and FileDescriptorStoreMax. Without systemd (no NOTIFY_SOCKET), nothing is held.

// fdNameMax is systemd's limit on a stored descriptor's name.
const fdNameMax = 255

// FDStore hands sockets to systemd's file descriptor store and takes back the ones passed
// to this start.
type FDStore struct {
	socket string // NOTIFY_SOCKET

	mu     sync.Mutex
	passed map[string][]*os.File // passed in at start and not yet taken, by name
}

// NewFDStore reads the descriptors systemd passed to this start. Call it once, early.
func NewFDStore() *FDStore {
	s := &FDStore{socket: os.Getenv("NOTIFY_SOCKET"), passed: map[string][]*os.File{}}
	pid, _ := strconv.Atoi(os.Getenv("LISTEN_PID"))
	count, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
	for _, v := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		os.Unsetenv(v)
	}
	if pid != os.Getpid() {
		count = 0 // meant for another process
	}
	for i := 0; i < count; i++ {
		fd := 3 + i // systemd passes them from fd 3 on (SD_LISTEN_FDS_START)
		syscall.CloseOnExec(fd)
		name := "unknown"
		if i < len(names) && names[i] != "" {
			name = names[i]
		}
		s.passed[name] = append(s.passed[name], os.NewFile(uintptr(fd), name))
	}
	if count > 0 {
		log.Printf("[FDSTORE] systemd passed %d held sockets to this start", count)
	}
	if s.socket == "" {
		log.Printf("[FDSTORE] Not run by systemd: RAM connections will drop when this service restarts")
	}
	return s
}

// Take returns the passed descriptors whose names start with prefix, by name, and removes
// them from the passed list. They stay in systemd's store under their names.
func (s *FDStore) Take(prefix string) map[string][]*os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]*os.File{}
	for name, files := range s.passed {
		if strings.HasPrefix(name, prefix) {
			out[name] = files
			delete(s.passed, name)
		}
	}
	return out
}

// ReleaseUntaken closes the passed descriptors nobody took and drops them from the store.
func (s *FDStore) ReleaseUntaken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, files := range s.passed {
		for _, f := range files {
			f.Close()
		}
		s.Remove(name)
		log.Printf("[FDSTORE] Released %d unused held socket(s) named %q", len(files), name)
		delete(s.passed, name)
	}
}

// Store hands a socket to systemd under a name. systemd keeps its own copy, so this
// process goes on using the socket as before.
func (s *FDStore) Store(name string, c syscall.Conn) error {
	if s.socket == "" {
		return nil
	}
	if len(name) > fdNameMax || strings.ContainsAny(name, ":\n") {
		return fmt.Errorf("invalid name %q for the file descriptor store", name)
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	if err := raw.Control(func(fd uintptr) {
		sendErr = s.notify("FDSTORE=1\nFDNAME="+name, int(fd))
	}); err != nil {
		return err
	}
	return sendErr
}

// Remove drops every socket stored under name and closes systemd's copies.
func (s *FDStore) Remove(name string) {
	if s.socket == "" {
		return
	}
	if err := s.notify("FDSTOREREMOVE=1\nFDNAME="+name, -1); err != nil {
		log.Printf("[FDSTORE] Could not remove %q from the store: %v", name, err)
	}
}

// notify sends one sd_notify message, with a descriptor attached when fd >= 0. A socket
// name starting with "@" is an abstract socket, which Go's syscall package handles.
func (s *FDStore) notify(msg string, fd int) error {
	sock, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(sock)
	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}
	return syscall.Sendmsg(sock, []byte(msg), oob, &syscall.SockaddrUnix{Name: s.socket}, 0)
}

// heldStream is what a held RAM connection needs after a restart, carried in its name in
// the store: an id that keeps names unique, when RAM connected, and the accounts it sent.
// Without the accounts, /ram-status would be empty after a restart and DK_Sync.ahk would
// restart RAM.
type heldStream struct {
	ID       int64    `json:"i"`
	Since    int64    `json:"s"` // Unix milliseconds
	Accounts []string `json:"a,omitempty"`

	name string // the name it was stored under, for one passed in at start
}

const heldStreamPrefix = "ram-conn."

// Name returns the store name for the stream: the prefix and base64url-encoded JSON, which
// has none of the characters systemd refuses in a name. When the accounts make the name too
// long, it is stored without them.
func (h heldStream) Name() string {
	data, _ := json.Marshal(h)
	name := heldStreamPrefix + base64.RawURLEncoding.EncodeToString(data)
	if len(name) > fdNameMax && len(h.Accounts) > 0 {
		log.Printf("[FDSTORE] Accounts %v don't fit in the held connection's name; it is held without them", h.Accounts)
		h.Accounts = nil
		return h.Name()
	}
	return name
}

// parseHeldStream reads a stream's details back from its store name.
func parseHeldStream(name string) (heldStream, bool) {
	var h heldStream
	if !strings.HasPrefix(name, heldStreamPrefix) {
		return h, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(name, heldStreamPrefix))
	if err != nil || json.Unmarshal(data, &h) != nil {
		return h, false
	}
	return h, true
}
