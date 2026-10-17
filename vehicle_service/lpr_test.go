package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// upload writes a file the way the camera does: photos as .tmp, then renamed.
func upload(t *testing.T, path, data string, viaTmp bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	target := path
	if viaTmp {
		target = path + ".tmp"
	}
	if err := os.WriteFile(target, []byte(data), 0664); err != nil {
		t.Fatal(err)
	}
	if viaTmp {
		if err := os.Rename(target, path); err != nil {
			t.Fatal(err)
		}
	}
}

// waitForJournal waits until the journal has n entries of kind.
func waitForJournal(t *testing.T, dir, kind string, n int) []map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		kinds, entries := readJournal(t, dir)
		if kinds[kind] >= n {
			var out []map[string]interface{}
			for _, e := range entries {
				if e["kind"] == kind {
					out = append(out, e)
				}
			}
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal has %d %q entries after 5 s, want %d", kinds[kind], kind, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLPRDrop(t *testing.T) {
	drop, jdir := t.TempDir(), t.TempDir()
	journal, err := NewJournal(jdir, 30)
	if err != nil {
		t.Fatal(err)
	}
	d := NewLPRDrop(drop, journal, t.Logf)
	d.pairWait = 300 * time.Millisecond
	d.SetGates(NewGates([]GateConfig{{Name: "South", LPRIP: "192.168.107.138"}}, nil))
	go d.Run()
	time.Sleep(200 * time.Millisecond) // let the watcher start

	// A file already there before the camera's folder existed is a test upload
	upload(t, filepath.Join(drop, "5c-f2-07-5f-fd-97", "TEST.txt"), "FTP Test", false)

	hour := filepath.Join(drop, "5c-f2-07-5f-fd-97", "VEHICE", "2026-10-16", "17")
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17.txt"),
		"\ufeffdevice name: S Gate Entry LPR mac: 5c-f2-07-5f-fd-97  ipaddr:192.168.107.138, License plate number:7JBM097 VEHICE time: 2026-10-16 17:12:05 \r\n", false)
	// A read: plate crop, then the scene photo a second later. The plate keeps its own text.
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-12-04-333_plate_7JB-M 097.jpg"), "plate", true)
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-12-04-333_src.jpg"), "scene", true)
	// A scene photo with no plate crop: a vehicle without a readable plate
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-20-00-001_src.jpg"), "scene", true)

	reads := waitForJournal(t, jdir, "plate", 2)
	first, second := reads[0], reads[1]
	if first["plate"] != "7JB-M 097" || first["capture_time"] != "2026-10-16 17:12:04.333" || first["missing"] != nil ||
		first["gate"] != "South" || first["direction"] != "entry" || first["camera"] != "5c-f2-07-5f-fd-97" ||
		first["camera_name"] != "S Gate Entry LPR" || first["scene_file"] == "" {
		t.Errorf("first read: %v", first)
	}
	if second["plate"] != "" || second["missing"] != "plate" || second["capture_time"] != "2026-10-16 17:20:00.001" {
		t.Errorf("read without a plate: %v", second)
	}

	others := waitForJournal(t, jdir, "lpr_file", 1)
	if filepath.Base(others[0]["file"].(string)) != "TEST.txt" {
		t.Errorf("other upload: %v", others[0])
	}

	// A vehicle without a readable plate
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-25-00-003_plate_Unknown000.jpg"), "vehicle", true)
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-25-00-003_src.jpg"), "scene", true)
	reads = waitForJournal(t, jdir, "plate", 3)
	if reads[2]["plate"] != "" || reads[2]["no_plate"] != true || reads[2]["camera_plate"] != "Unknown000" || reads[2]["missing"] != nil {
		t.Errorf("vehicle without a plate: %v", reads[2])
	}

	// A plate crop whose scene photo never comes
	upload(t, filepath.Join(hour, "VEHICE_2026-10-16-17-30-00-002_plate_ABC123.jpg"), "plate", true)
	reads = waitForJournal(t, jdir, "plate", 4)
	if reads[3]["plate"] != "ABC123" || reads[3]["missing"] != "scene" {
		t.Errorf("read without a scene photo: %v", reads[3])
	}

	// Each upload counts once, though a new folder's scan and the files' own notices both see it
	time.Sleep(2 * d.pairWait)
	if kinds, _ := readJournal(t, jdir); kinds["plate"] != 4 || kinds["lpr_file"] != 1 {
		t.Errorf("journal has %d reads and %d other uploads, want 4 and 1", kinds["plate"], kinds["lpr_file"])
	}
}

// The camera uploads its hourly summary after the photos; a read from a camera not known yet
// waits for it, and cameras already named in the folder's summaries are known from the start.
func TestLPRDropSummaryAfterPhotos(t *testing.T) {
	drop, jdir := t.TempDir(), t.TempDir()
	journal, err := NewJournal(jdir, 30)
	if err != nil {
		t.Fatal(err)
	}
	gates := NewGates([]GateConfig{{Name: "North", LPRIP: "192.168.107.140"}}, []ExitLaneConfig{{Name: "South", LPRIP: "192.168.107.137"}})
	summary := func(name, mac, ip string) string {
		return "\ufeffdevice name: " + name + " mac: " + mac + "  ipaddr:" + ip + ", License plate number:X VEHICE time: 2026-10-17 08:45:43 \r\n"
	}
	// The south exit camera's summary is there before the reader starts
	exitHour := filepath.Join(drop, "5c-f2-07-5f-fd-be", "VEHICE", "2026-10-17", "8")
	upload(t, filepath.Join(exitHour, "VEHICE_2026-10-17-08.txt"), summary("S Gate Exit LPR", "5c-f2-07-5f-fd-be", "192.168.107.137"), false)

	d := NewLPRDrop(drop, journal, t.Logf)
	d.SetGates(gates)
	go d.Run()
	time.Sleep(200 * time.Millisecond)

	// North entrance: photos, then the summary
	hour := filepath.Join(drop, "5c-f2-07-5f-fd-9d", "VEHICE", "2026-10-17", "8")
	upload(t, filepath.Join(hour, "VEHICE_2026-10-17-08-45-42-091_plate_39357Y2.jpg"), "plate", true)
	upload(t, filepath.Join(hour, "VEHICE_2026-10-17-08-45-42-091_src.jpg"), "scene", true)
	time.Sleep(300 * time.Millisecond)
	upload(t, filepath.Join(hour, "VEHICE_2026-10-17-08.txt"), summary("N Gate Entry LPR", "5c-f2-07-5f-fd-9d", "192.168.107.140"), false)
	// South exit: known from the start
	upload(t, filepath.Join(exitHour, "VEHICE_2026-10-17-08-50-00-001_plate_8RGW713.jpg"), "plate", true)
	upload(t, filepath.Join(exitHour, "VEHICE_2026-10-17-08-50-00-001_src.jpg"), "scene", true)

	reads := waitForJournal(t, jdir, "plate", 2)
	got := map[string]string{}
	for _, r := range reads {
		got[r["plate"].(string)] = fmt.Sprint(r["gate"], " ", r["direction"])
	}
	if got["39357Y2"] != "North entry" || got["8RGW713"] != "South exit" {
		t.Errorf("lanes: %v", got)
	}
}

func TestLPRCaptureTimeText(t *testing.T) {
	if got := lprCaptureTimeText("2026-10-17-06-31-04-333"); got != "2026-10-17 06:31:04.333" {
		t.Errorf("got %q", got)
	}
	if got := lprCaptureTimeText("odd"); got != "odd" {
		t.Errorf("got %q", got)
	}
}
