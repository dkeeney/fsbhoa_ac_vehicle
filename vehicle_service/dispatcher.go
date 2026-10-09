package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dispatcher delivers finished events to WordPress through an on-disk queue, so an event
// survives WordPress being down or the service restarting. Each event is one JSON file in
// QueueDir, named by its event ID so files sort oldest first. A file is deleted only after
// WordPress answers 2xx. Rejections that retrying can't fix (400, 409, 422) are moved to
// QueueDir/failed for a person to look at; anything else is retried, in order.
type Dispatcher struct {
	cfg       *ServiceConfig
	dir       string
	failedDir string
	client    *http.Client
	wake      chan struct{}
}

const retryInterval = 30 * time.Second

func NewDispatcher(cfg *ServiceConfig) (*Dispatcher, error) {
	d := &Dispatcher{
		cfg:       cfg,
		dir:       cfg.QueueDir,
		failedDir: filepath.Join(cfg.QueueDir, "failed"),
		client: &http.Client{
			Timeout: 10 * time.Second,
			// A redirect (such as http -> https) would turn the POST into a GET; report it instead
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		wake: make(chan struct{}, 1),
	}
	if err := os.MkdirAll(d.failedDir, 0750); err != nil {
		return nil, fmt.Errorf("creating queue dir %s: %w", d.failedDir, err)
	}
	return d, nil
}

// Enqueue writes one event to the queue (temp file + rename, so the sender never sees a
// partial file) and wakes the sender.
func (d *Dispatcher) Enqueue(eventID string, payload []byte) error {
	final := filepath.Join(d.dir, eventID+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0640); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	select {
	case d.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
	return nil
}

// Run sends queued events whenever one is added, and retries every retryInterval.
func (d *Dispatcher) Run() {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	d.drain() // events left from before a restart
	for {
		select {
		case <-d.wake:
		case <-ticker.C:
		}
		d.drain()
	}
}

func (d *Dispatcher) queued() []string {
	files, err := filepath.Glob(filepath.Join(d.dir, "*.json"))
	if err != nil {
		log.Printf("[ERROR] Listing queue %s: %v", d.dir, err)
		return nil
	}
	sort.Strings(files)
	return files
}

// drain posts queued events oldest first, stopping at the first one that should be retried
// so events reach WordPress in order.
func (d *Dispatcher) drain() {
	files := d.queued()
	if len(files) == 0 {
		return
	}

	// Fail closed: never post without knowing which environment this server is
	if !validEnvironment(d.cfg.Environment) {
		log.Printf("[ERROR] Not sending %d queued event(s): environment is %q, not 'testbed' or 'production'. Set FSBHOA_AC_ENVIRONMENT in wp-config.php and save the Vehicle Service settings.", len(files), d.cfg.Environment)
		return
	}

	for i, path := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[ERROR] Reading queued event %s: %v", path, err)
			continue
		}

		status, body, err := d.post(payload)
		name := filepath.Base(path)
		switch {
		case err != nil:
			log.Printf("[WARN] Could not reach WordPress (%v); %d event(s) queued, retrying in %s", err, len(files)-i, retryInterval)
			return
		case status >= 200 && status < 300:
			os.Remove(path)
			log.Printf("[DISPATCH] Stored %s via WordPress REST API", strings.TrimSuffix(name, ".json"))
		case status == http.StatusBadRequest || status == http.StatusConflict || status == http.StatusUnprocessableEntity:
			log.Printf("[ERROR] WordPress rejected %s with HTTP %d: %s. Moved to %s", name, status, body, d.failedDir)
			if err := os.Rename(path, filepath.Join(d.failedDir, name)); err != nil {
				log.Printf("[ERROR] Moving %s to failed: %v", name, err)
				return
			}
		default:
			log.Printf("[WARN] WordPress returned HTTP %d for %s: %s. %d event(s) queued, retrying in %s", status, name, body, len(files)-i, retryInterval)
			return
		}
	}
}

// post sends one event and returns the HTTP status and the start of the response body.
func (d *Dispatcher) post(payload []byte) (int, string, error) {
	// Stamp the environment at send time, not when the event was queued: it says which
	// server is sending, and an event queued while it was unset must still go through
	// once it is fixed. WordPress refuses events from the other environment.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return http.StatusBadRequest, "queued event is not valid JSON: " + err.Error(), nil
	}
	env, _ := json.Marshal(d.cfg.Environment)
	fields["environment"] = env
	payload, err := json.Marshal(fields)
	if err != nil {
		return 0, "", err
	}

	req, err := http.NewRequest("POST", d.cfg.WPWebhookURL, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", d.cfg.APIKey)
	// WordPress runs on this machine; the Host header picks this server's own site
	req.Host = d.cfg.WordPressHost

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return resp.StatusCode, strings.TrimSpace(string(body)), nil
}

func validEnvironment(env string) bool {
	return env == "testbed" || env == "production"
}
