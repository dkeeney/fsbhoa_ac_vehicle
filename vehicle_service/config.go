package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// ServiceConfig matches the JSON structure written by WordPress to /var/lib/fsbhoa/vehicle_service.json
type ServiceConfig struct {
	DaemonHost         string `json:"daemon_host"`
	DaemonPort         int    `json:"daemon_port"`
	APIKey             string `json:"api_key"` // core's Access Verification API Key, sent as X-API-KEY
	CorrelationWindow  int    `json:"correlation_window"`
	EnableDebugLogging int    `json:"enable_debug_logging"`
	WordPressHost      string `json:"wordpress_host"` // this server's own site, sent as the Host header
	Environment        string `json:"environment"`    // FSBHOA_AC_ENVIRONMENT: testbed or production
	QueueDir           string `json:"queue_dir"`      // events waiting for WordPress

	// Traffic study (CLAUDE.md, "Build order", step 0). The address lists stand in for the
	// per-lane Gates settings until those exist. This machine is always allowed.
	RAMPort       int      `json:"ram_port"`       // DoorKing RAM Live Streaming listener
	RAMSources    []string `json:"ram_sources"`    // addresses RAM may stream from
	DeviceSources []string `json:"device_sources"` // addresses allowed to call the webhooks (Shellys, cameras)
	JournalDir    string   `json:"journal_dir"`    // raw-input journal, one file per day
	JournalDays   int      `json:"journal_days"`   // journal files kept this many days

	// Local runtime fields (not in JSON, populated via flags/defaults)
	WatchDir       string        `json:"-"`
	ConfigPath     string        `json:"-"`
	WindowDuration time.Duration `json:"-"`
	WPWebhookURL   string        `json:"-"`
}

// LoadConfig reads the JSON configuration from disk and applies defaults
func LoadConfig(path string) (*ServiceConfig, error) {
	// Defaults
	cfg := &ServiceConfig{
		DaemonHost:         "127.0.0.1",
		DaemonPort:         8088,
		CorrelationWindow:  15,
		EnableDebugLogging: 0,
		WordPressHost:      "127.0.0.1",
		WatchDir:           "/home/pi/lpr_ftp_drop",
		QueueDir:           "/var/lib/fsbhoa/vehicle_queue",
		RAMPort:            8089,
		JournalDir:         "/var/lib/fsbhoa/vehicle_journal",
		JournalDays:        30,
		ConfigPath:         path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[CONFIG] %s not found, using compiled defaults", path)
		} else {
			return nil, fmt.Errorf("failed reading config %s: %w", path, err)
		}
	} else {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed parsing config JSON: %w", err)
		}
		log.Printf("[CONFIG] Loaded configuration from %s", path)
	}

	// Derived runtime values
	cfg.WindowDuration = time.Duration(cfg.CorrelationWindow) * time.Second
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = 15 * time.Second
	}

	// WordPress always runs on this machine, so post to it locally. The Host header
	// (wordpress_host) picks this server's own site. Never a remote host: the testbed
	// must not be able to post to production.
	if cfg.WordPressHost == "" {
		cfg.WordPressHost = "127.0.0.1"
	}
	cfg.WPWebhookURL = "http://127.0.0.1/wp-json/fsbhoa/v1/vehicle-event"

	if cfg.RAMPort <= 0 || cfg.RAMPort > 65535 {
		cfg.RAMPort = 8089
	}
	if cfg.JournalDays <= 0 {
		cfg.JournalDays = 30
	}

	if cfg.APIKey == "" {
		log.Printf("[CONFIG] WARNING: api_key is empty; WordPress will refuse every vehicle event. Set the Access Verification API Key in core General settings and save.")
	}

	return cfg, nil
}
