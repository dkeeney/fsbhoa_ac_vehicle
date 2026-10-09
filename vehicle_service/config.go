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
	AllowedDaemonIPs   string `json:"allowed_daemon_ips"`
	CorrelationWindow  int    `json:"correlation_window"`
	ImageRetentionDays int    `json:"image_retention_days"`
	EnableDebugLogging int    `json:"enable_debug_logging"`
	WordPressHost      string `json:"wordpress_host"`

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
		ImageRetentionDays: 90,
		EnableDebugLogging: 0,
		WordPressHost:      "127.0.0.1",
		WatchDir:           "/home/pi/lpr_ftp_drop",
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

	// Construct webhook URL against local or remote WordPress
	if cfg.WordPressHost == "" {
		cfg.WordPressHost = "127.0.0.1"
	}
	cfg.WPWebhookURL = fmt.Sprintf("http://%s/wp-json/fsbhoa/v1/vehicle-event", cfg.WordPressHost)

	if cfg.APIKey == "" {
		log.Printf("[CONFIG] WARNING: api_key is empty; WordPress will refuse every vehicle event. Set the Access Verification API Key in core General settings and save.")
	}

	return cfg, nil
}
