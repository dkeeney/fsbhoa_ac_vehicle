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

	// Lanes and devices (CLAUDE.md, "Lanes and devices"). This machine is always
	// allowed. All of these are applied without a restart when the file changes.
	Gates          []GateConfig     `json:"gates"`           // entrance lanes
	ExitLanes      []ExitLaneConfig `json:"exit_lanes"`      // exit lanes
	DeviceSources  []string         `json:"device_sources"`  // extra webhook addresses (older configs); the gates' devices are always allowed
	DoorKingEvents bool             `json:"doorking_events"` // send DoorKing authentications as events on their own (stopgap)
	PlateEvents    bool             `json:"plate_events"`    // send LPR camera reads as events on their own (stopgap)
	JournalDir     string           `json:"journal_dir"`     // raw-input journal, one file per day
	JournalDays    int              `json:"journal_days"`    // journal files kept this many days
	// RAM's Live Streaming Disk File (ramfile.go), the source of DoorKing records. Empty: none.
	RAMFile string `json:"ram_file"`
	// Where the LPR cameras' FTP uploads land (lpr.go; lpr_ftp/ sets up the FTP server)
	LPRDropDir string `json:"lpr_drop_dir"`
	// Motion detection on the overview and keypad cameras (motion.go). No regions: none.
	MotionRegions     []MotionRegion `json:"motion_regions"`
	OverviewFrameDir  string         `json:"overview_frame_dir"`  // frames kept for each pass of motion
	OverviewFrameDays int            `json:"overview_frame_days"` // kept this many days
	// Car counting from the overview cameras' frames (count.go). No zones: none.
	CountZones []CountZone `json:"count_zones"`
	// The gates' Frigate (frigate.go): its MQTT broker, host:port, and topic prefix. Empty: none.
	FrigateBroker string `json:"frigate_broker"`
	FrigateTopic  string `json:"frigate_topic"`
	// The NVR that records the cameras (nvr.go), for the live monitor's Video button. Empty: none.
	NVRURL      string `json:"nvr_url"`
	NVRUser     string `json:"nvr_user"`
	NVRPassword string `json:"nvr_password"`

	// Local runtime fields (not in JSON, populated via flags/defaults)
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
		LPRDropDir:         "/var/lib/fsbhoa/lpr_drop",
		OverviewFrameDir:   "/var/lib/fsbhoa/overview_frames",
		OverviewFrameDays:  3,
		QueueDir:           "/var/lib/fsbhoa/vehicle_queue",
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

	if cfg.LPRDropDir == "" {
		cfg.LPRDropDir = "/var/lib/fsbhoa/lpr_drop"
	}

	if cfg.OverviewFrameDir == "" {
		cfg.OverviewFrameDir = "/var/lib/fsbhoa/overview_frames"
	}
	if cfg.OverviewFrameDays <= 0 {
		cfg.OverviewFrameDays = 3
	}

	if cfg.JournalDays <= 0 {
		cfg.JournalDays = 30
	}

	if cfg.APIKey == "" {
		log.Printf("[CONFIG] WARNING: api_key is empty; WordPress will refuse every vehicle event. Set the Access Verification API Key in core General settings and save.")
	}

	return cfg, nil
}
