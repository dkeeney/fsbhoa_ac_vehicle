package main

import (
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchConfig re-reads the config file whenever WordPress rewrites it (it writes a temp
// file and renames it into place), and calls apply with the new config. Settings that can
// change without a restart are applied this way.
func WatchConfig(path string, apply func(*ServiceConfig)) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[ERROR] Config watcher: %v; settings changes need a restart", err)
		return
	}
	defer watcher.Close()
	// Watch the directory: the rename replaces the file, which would end a watch on it
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		log.Printf("[ERROR] Config watcher on %s: %v; settings changes need a restart", filepath.Dir(path), err)
		return
	}

	var debounce *time.Timer
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != filepath.Clean(path) || event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(300*time.Millisecond, func() {
				cfg, err := LoadConfig(path)
				if err != nil {
					log.Printf("[ERROR] Config changed but could not be read (%v); keeping the current settings", err)
					return
				}
				apply(cfg)
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[ERROR] Config watcher: %v", err)
		}
	}
}

// restartNeeded lists the settings that differ between two configs but are only read at
// start.
func restartNeeded(old, cur *ServiceConfig) []string {
	var changed []string
	check := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}
	check("daemon_port", old.DaemonPort != cur.DaemonPort)
	check("api_key", old.APIKey != cur.APIKey)
	check("correlation_window", old.CorrelationWindow != cur.CorrelationWindow)
	check("enable_debug_logging", old.EnableDebugLogging != cur.EnableDebugLogging)
	check("wordpress_host", old.WordPressHost != cur.WordPressHost)
	check("environment", old.Environment != cur.Environment)
	check("queue_dir", old.QueueDir != cur.QueueDir)
	check("journal_dir", old.JournalDir != cur.JournalDir)
	check("journal_days", old.JournalDays != cur.JournalDays)
	check("ram_file", old.RAMFile != cur.RAMFile)
	check("lpr_drop_dir", old.LPRDropDir != cur.LPRDropDir)
	check("overview_frame_dir", old.OverviewFrameDir != cur.OverviewFrameDir)
	check("overview_frame_days", old.OverviewFrameDays != cur.OverviewFrameDays)
	check("frigate_broker", old.FrigateBroker != cur.FrigateBroker)
	check("frigate_topic", old.FrigateTopic != cur.FrigateTopic)
	return changed
}
