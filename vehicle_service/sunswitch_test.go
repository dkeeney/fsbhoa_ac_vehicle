package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Sunrise and sunset at the Lodge, Bakersfield (35.40 N, 118.90 W), within 2 minutes of the
// published times.
func TestSunTimes(t *testing.T) {
	for _, c := range []struct{ day, rise, set string }{
		{"2026-11-01", "06:16", "17:01"}, // the first day of standard time
		{"2026-10-31", "07:15", "18:02"},
		{"2026-06-21", "05:40", "20:13"},
		{"2026-12-21", "07:00", "16:46"},
	} {
		day, _ := time.ParseInLocation("2006-01-02", c.day, time.Local)
		rise, set := sunTimes(day.Add(12*time.Hour), 35.40, -118.90)
		for _, p := range []struct {
			got  time.Time
			want string
		}{{rise, c.rise}, {set, c.set}} {
			want, _ := time.ParseInLocation("2006-01-02 15:04", c.day+" "+p.want, time.Local)
			if d := p.got.Sub(want); d < -2*time.Minute || d > 2*time.Minute {
				t.Errorf("%s: got %s, want about %s", c.day, p.got.Format("15:04"), p.want)
			}
		}
	}
}

// fakeSpeco answers SetImageConfigFileInfo and GetImageConfigFileInfo like the LPR cameras.
type fakeSpeco struct {
	mu                   sync.Mutex
	program, day, night  string
	sets                 int
	answerDay, wrongAuth bool
}

func (f *fakeSpeco) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "s1 x" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	field := func(re string) string {
		if m := regexp.MustCompile(re).FindSubmatch(body); m != nil {
			return string(m[1])
		}
		return ""
	}
	switch r.URL.Path {
	case "/SetImageConfigFileInfo":
		f.sets++
		f.program, f.day, f.night = field(`<program[^>]*>([a-z]+)<`), field(`dayTime[^>]*><!\[CDATA\[([0-9:]+)`), field(`nightTime[^>]*><!\[CDATA\[([0-9:]+)`)
		if f.answerDay {
			f.day = "07:10" // a camera that kept its old time
		}
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><config version="1.7" xmlns="http://www.ipc.com/ver10" status="success" errorCode="0"/>`)
	case "/GetImageConfigFileInfo":
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<config version="1.7" xmlns="http://www.ipc.com/ver10">
<program type="progType">`+f.program+`</program>
<dayTime type="string"><![CDATA[`+f.day+`]]></dayTime>
<nightTime type="string"><![CDATA[`+f.night+`]]></nightTime>
</config>`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestSunSwitcher(t *testing.T, env string, cam *fakeSpeco) (*SunSwitcher, *time.Time, string) {
	dir := t.TempDir()
	journal, err := NewJournal(filepath.Join(dir, "journal"), 30)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(cam)
	t.Cleanup(srv.Close)
	s := NewSunSwitcher(journal)
	s.loginFile = filepath.Join(dir, "lpr_ftp")
	os.WriteFile(s.loginFile, []byte("FTP Server Password: x\nSpeco Cameras: admin s1 x\nAll other cameras: admin p\n"), 0600)
	s.base = func(string) string { return srv.URL }
	now := time.Date(2026, 11, 1, 11, 43, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	s.SetConfig(&ServiceConfig{Environment: env, SiteLatitude: 35.40, SiteLongitude: -118.90,
		SunSwitch: []SunSwitch{{Camera: "192.168.107.137", NightAfter: 10, DayBefore: 5}}})
	return s, &now, filepath.Join(dir, "journal")
}

func journalText(t *testing.T, dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var b strings.Builder
	for _, f := range files {
		data, _ := os.ReadFile(f)
		b.Write(data)
	}
	return b.String()
}

// On production, the camera is set to switch by time at sunset + 10 min and tomorrow's sunrise
// - 5 min, read back, once a day, only in the midday hours.
func TestSunSwitchSets(t *testing.T) {
	cam := &fakeSpeco{program: "auto", day: "00:00", night: "23:59"}
	s, now, jdir := newTestSunSwitcher(t, "production", cam)
	s.check()
	if cam.sets != 1 || cam.program != "time" || cam.day != "06:12" || cam.night != "17:11" {
		t.Fatalf("set %d times: %s %s/%s, want once: time 06:12/17:11 (sunset 17:01, tomorrow's sunrise 06:17)", cam.sets, cam.program, cam.day, cam.night)
	}
	s.check()
	if cam.sets != 1 {
		t.Fatalf("set again the same day")
	}
	if j := journalText(t, jdir); !strings.Contains(j, `"status":"set"`) || strings.Contains(j, "s1 x") {
		t.Fatalf("journal: %s", j)
	}
	*now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local) // morning: too close to the switch to day
	s.check()
	if cam.sets != 1 {
		t.Fatalf("set outside the midday hours")
	}
	*now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	s.check()
	if cam.sets != 2 || cam.night != "17:10" {
		t.Fatalf("next day: set %d times, night %s, want twice, 17:10", cam.sets, cam.night)
	}

	// Taken off the list: back to switching by light
	s.SetConfig(&ServiceConfig{Environment: "production", SiteLatitude: 35.40, SiteLongitude: -118.90})
	s.check()
	if cam.program != "auto" || cam.day != "00:00" || cam.night != "23:59" {
		t.Fatalf("removed: %s %s/%s, want auto 00:00/23:59", cam.program, cam.day, cam.night)
	}
}

// A read-back that doesn't match is an error, and is tried again at the next check.
func TestSunSwitchReadBack(t *testing.T) {
	cam := &fakeSpeco{answerDay: true}
	s, _, jdir := newTestSunSwitcher(t, "production", cam)
	s.check()
	s.check()
	if cam.sets != 2 || !strings.Contains(journalText(t, jdir), "read back time 07:10/17:11") {
		t.Fatalf("set %d times; journal: %s", cam.sets, journalText(t, jdir))
	}
}

// Anywhere but production nothing is sent to the camera: the times are only journaled.
func TestSunSwitchNotProduction(t *testing.T) {
	for _, env := range []string{"testbed", ""} {
		cam := &fakeSpeco{}
		s, _, jdir := newTestSunSwitcher(t, env, cam)
		s.check()
		if cam.sets != 0 {
			t.Fatalf("%q: the camera was set", env)
		}
		if j := journalText(t, jdir); !strings.Contains(j, `"skipped"`) || !strings.Contains(j, `"night":"17:11"`) {
			t.Fatalf("%q: journal: %s", env, j)
		}
	}
}
