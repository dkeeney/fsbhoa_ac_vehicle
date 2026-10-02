package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config holds runtime parameters passed via CLI flags or config file
type Config struct {
	WatchDir       string
	HTTPPort       string
	WPWebhookURL   string
	APIKey         string
	WindowDuration time.Duration
}

// VehicleEvent aggregates the three disparate data streams
type VehicleEvent struct {
	EventID        string    `json:"event_id"`
	Timestamp      time.Time `json:"timestamp"`
	LicensePlate   string    `json:"license_plate"`
	PlateImageRel  string    `json:"plate_image_rel"`
	SceneImageRel  string    `json:"scene_image_rel"`
	DoorKingCode   string    `json:"doorking_code,omitempty"`
	DoorKingDevice string    `json:"doorking_device,omitempty"`
	LoopActive     bool      `json:"loop_active"`
}

type GateStateManager struct {
	mu           sync.Mutex
	cfg          *ServiceConfig
	currentEvent *VehicleEvent
	expireTimer  *time.Timer
	plateRegex   *regexp.Regexp
}

func NewGateStateManager(cfg *ServiceConfig) *GateStateManager {
	return &GateStateManager{
		cfg:        cfg,
		plateRegex: regexp.MustCompile(`VEHICE_(\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d+)_plate_([A-Z0-9]+)\.jpg$`),
	}
}

// HandleLoopTrigger is called via internal HTTP endpoint from Shelly Pro 1
func (gsm *GateStateManager) HandleLoopTrigger(active bool) {
	gsm.mu.Lock()
	defer gsm.mu.Unlock()

	log.Printf("[STATE] Loop Detector State Change: %v", active)
	if active {
		gsm.ensureActiveEvent()
		gsm.currentEvent.LoopActive = true
	}
}

// HandleDoorKingEntry is called when DoorKing emits an access grant/PIN
func (gsm *GateStateManager) HandleDoorKingEntry(device, code string) {
	gsm.mu.Lock()
	defer gsm.mu.Unlock()

	log.Printf("[STATE] DoorKing Grant: Device=%s Code=%s", device, code)
	gsm.ensureActiveEvent()
	gsm.currentEvent.DoorKingDevice = device
	gsm.currentEvent.DoorKingCode = code
}

// HandlePlateImage processes the newly written Speco FTP file
func (gsm *GateStateManager) HandlePlateImage(fullPath string) {
	gsm.mu.Lock()
	defer gsm.mu.Unlock()

	filename := filepath.Base(fullPath)
	matches := gsm.plateRegex.FindStringSubmatch(filename)
	if len(matches) < 3 {
		return // Not a plate crop image
	}

	plateStr := matches[2]
	log.Printf("[STATE] Speco LPR Capture: Plate=%s (File: %s)", plateStr, filename)

	gsm.ensureActiveEvent()
	gsm.currentEvent.LicensePlate = plateStr
	gsm.currentEvent.PlateImageRel = fullPath

	// Infer scene file path
	sceneFile := gsm.plateRegex.ReplaceAllString(fullPath, "VEHICE_${1}_src.jpg")
	if _, err := os.Stat(sceneFile); err == nil {
		gsm.currentEvent.SceneImageRel = sceneFile
	}
}

func (gsm *GateStateManager) ensureActiveEvent() {
	if gsm.currentEvent == nil {
		gsm.currentEvent = &VehicleEvent{
			EventID:   fmt.Sprintf("evt_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		}
	}

	// Reset window flush timer
	if gsm.expireTimer != nil {
		gsm.expireTimer.Stop()
	}
	gsm.expireTimer = time.AfterFunc(gsm.cfg.WindowDuration, gsm.flushEvent)
}

func (gsm *GateStateManager) flushEvent() {
	gsm.mu.Lock()
	if gsm.currentEvent == nil {
		gsm.mu.Unlock()
		return
	}
	eventToShip := *gsm.currentEvent
	gsm.currentEvent = nil
	gsm.mu.Unlock()

	log.Printf("[DISPATCH] Window closed. Correlating Event: Plate=%s, Auth=%s, Loop=%v",
		eventToShip.LicensePlate, eventToShip.DoorKingCode, eventToShip.LoopActive)

	// Read and base64-encode the plate crop
	var plateB64 string
	if eventToShip.PlateImageRel != "" {
		if data, err := os.ReadFile(eventToShip.PlateImageRel); err == nil {
			plateB64 = base64.StdEncoding.EncodeToString(data)
			_ = os.Remove(eventToShip.PlateImageRel) // Clean up drop dir
		} else {
			log.Printf("[WARN] Failed reading plate image %s: %v", eventToShip.PlateImageRel, err)
		}
	}

	// Read and base64-encode the full scene context image
	var contextB64 string
	if eventToShip.SceneImageRel != "" {
		if data, err := os.ReadFile(eventToShip.SceneImageRel); err == nil {
			contextB64 = base64.StdEncoding.EncodeToString(data)
			_ = os.Remove(eventToShip.SceneImageRel) // Clean up drop dir
		} else {
			log.Printf("[WARN] Failed reading scene image %s: %v", eventToShip.SceneImageRel, err)
		}
	}

	// Flag circumvention if loop tripped without any credential/auth
	isCircumvention := 0
	if eventToShip.LoopActive && eventToShip.DoorKingCode == "" {
		isCircumvention = 1
	}

	// Build the exact payload schema expected by fsbhoa_ac_vehicle.php
	payloadData := map[string]interface{}{
		"gate_identifier":   eventToShip.DoorKingDevice,
		"auth_id":           eventToShip.DoorKingCode,
		"lpr_plate":         eventToShip.LicensePlate,
		"is_circumvention":  isCircumvention,
		"lpr_image_b64":     plateB64,
		"context_image_b64": contextB64,
		"raw_details": map[string]interface{}{
			"event_id":    eventToShip.EventID,
			"loop_active": eventToShip.LoopActive,
		},
	}

	payload, err := json.Marshal(payloadData)
	if err != nil {
		log.Printf("[ERROR] JSON marshal failed: %v", err)
		return
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequest("POST", gsm.cfg.WPWebhookURL, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("[ERROR] Request build failed: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Host = gsm.cfg.WordPressHost
	if gsm.cfg.IngestSecret != "" {
		req.Header.Set("Authorization", "Bearer "+gsm.cfg.IngestSecret)
		req.Header.Set("X-FSBHOA-Secret", gsm.cfg.IngestSecret)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[ERROR] Failed to post event to WordPress: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[WARN] WordPress ingest returned HTTP %d", resp.StatusCode)
	} else {
		log.Printf("[DISPATCH] Successfully stored event to database via WordPress REST API")
	}
}

// WatchDropDir listens for new files delivered by the Speco camera via FTP
func (gsm *GateStateManager) WatchDropDir() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("[FATAL] Watcher creation failed: %v", err)
	}
	defer watcher.Close()

	// Walk and watch all subdirectories
	err = filepath.Walk(gsm.cfg.WatchDir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		log.Printf("[WARN] Error walking watch dir: %v", err)
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				fi, err := os.Stat(event.Name)
				if err == nil && fi.IsDir() {
					watcher.Add(event.Name)
				} else if err == nil {
					// Debounce slightly to ensure FTP completed file write
					go func(path string) {
						time.Sleep(250 * time.Millisecond)
						gsm.HandlePlateImage(path)
					}(event.Name)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[ERROR] fsnotify error: %v", err)
		}
	}
}

func main() {
	configPath := flag.String("config", "/var/lib/fsbhoa/vehicle_service.json", "Path to JSON config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[FATAL] Configuration load failed: %v", err)
	}

	gsm := NewGateStateManager(cfg)

	// Ensure drop directory exists before starting watcher
	if err := os.MkdirAll(cfg.WatchDir, 0755); err != nil {
		log.Printf("[WARN] Could not create watch directory %s: %v", cfg.WatchDir, err)
	}

	// Start drop directory watcher
	go gsm.WatchDropDir()

	mux := http.NewServeMux()

	// WordPress health probe endpoint
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service": "vehicle_service",
			"status":  "healthy",
			"uptime":  time.Now().Unix(),
		})
	}
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/status", healthHandler)

	// Shelly Pro 1 Loop Detector Webhook
	mux.HandleFunc("/webhook/loop", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state") == "on" || r.URL.Query().Get("state") == "1"
		gsm.HandleLoopTrigger(state)
		w.WriteHeader(http.StatusOK)
	})

	// DoorKing Event Receiver
	mux.HandleFunc("/webhook/doorking", func(w http.ResponseWriter, r *http.Request) {
		device := r.URL.Query().Get("device")
		code := r.URL.Query().Get("code")
		gsm.HandleDoorKingEntry(device, code)
		w.WriteHeader(http.StatusOK)
	})

	listenAddr := fmt.Sprintf("%s:%d", cfg.DaemonHost, cfg.DaemonPort)
	log.Printf("FSBHOA Vehicle Service listening on %s (Watch: %s)", listenAddr, cfg.WatchDir)
	log.Fatal(http.ListenAndServe(listenAddr, mux))
}
