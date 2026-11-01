package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SunSwitch is an LPR camera whose switch between its day and night profiles follows the sun
// (the "Day/Night by the Sun" setting, config "sun_switch"; TODO #37). The Speco cameras switch
// by light, by time, or not at all; switching by light, South Exit's .137 didn't apply its night
// profile's fixed shutter until the profile was saved again, so its plates were white all
// night, but switching by time it did (2026-10-30 and 31). A time fixed in the camera drifts
// from the sun, so it is set each day.
type SunSwitch struct {
	Camera     string `json:"camera"`
	NightAfter int    `json:"night_after"` // minutes after sunset it switches to night
	DayBefore  int    `json:"day_before"`  // minutes before sunrise it switches to day
}

const (
	// The times are set once a day in these hours, with the camera in its day profile and both
	// switches away: tomorrow's day time and today's night time are then on either side of now,
	// so a new time can't make the camera switch.
	sunSwitchFrom, sunSwitchTo = 11, 15
	sunSwitchEvery             = 10 * time.Minute
	sunSwitchTimeout           = 15 * time.Second
	specoLoginLabel            = "Speco Cameras:"
)

// SunSwitcher sets its cameras' switch times each day. It writes to the cameras, so only on
// production (fail closed); elsewhere it journals what it would set.
type SunSwitcher struct {
	mu        sync.Mutex
	cams      []SunSwitch
	lat, lon  float64
	hasSite   bool
	env       string
	applied   map[string]string // camera: the day and times last set (or journaled), so once a day
	removed   map[string]bool   // cameras taken off the list, to put back on switching by light
	journal   *Journal
	loginFile string
	client    *http.Client
	now       func() time.Time
	base      func(ip string) string // the camera's address, "http://<ip>" (tests replace it)
}

func NewSunSwitcher(journal *Journal) *SunSwitcher {
	s := &SunSwitcher{
		applied: map[string]string{},
		removed: map[string]bool{},
		journal: journal,
		client:  &http.Client{Timeout: sunSwitchTimeout},
		now:     time.Now,
		base:    func(ip string) string { return "http://" + ip },
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.loginFile = filepath.Join(home, cameraLoginFile)
	}
	return s
}

// SetConfig takes the cameras, the site and the environment; a camera no longer listed goes
// back to switching by light at the next check.
func (s *SunSwitcher) SetConfig(c *ServiceConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]bool{}
	for _, sw := range c.SunSwitch {
		next[sw.Camera] = true
		delete(s.removed, sw.Camera)
	}
	for _, sw := range s.cams {
		if !next[sw.Camera] {
			s.removed[sw.Camera] = true
		}
	}
	s.cams = append([]SunSwitch(nil), c.SunSwitch...)
	s.lat, s.lon = c.SiteLatitude, c.SiteLongitude
	s.hasSite = c.SiteLatitude != 0 || c.SiteLongitude != 0
	s.env = c.Environment
	s.applied = map[string]string{} // the settings changed: set again at the next check
}

// Run checks every sunSwitchEvery until stop is closed.
func (s *SunSwitcher) Run(stop <-chan struct{}) {
	t := time.NewTicker(sunSwitchEvery)
	defer t.Stop()
	for {
		s.check()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func (s *SunSwitcher) check() {
	now := s.now()
	if now.Hour() < sunSwitchFrom || now.Hour() >= sunSwitchTo {
		return
	}
	s.mu.Lock()
	cams := append([]SunSwitch(nil), s.cams...)
	var removed []string
	for ip := range s.removed {
		removed = append(removed, ip)
	}
	lat, lon, hasSite, env := s.lat, s.lon, s.hasSite, s.env
	s.mu.Unlock()

	for _, ip := range removed {
		// Back to switching by light, as the cameras came (day and night times as they were)
		if s.set(ip, "auto", "00:00", "23:59", env, "") {
			s.mu.Lock()
			delete(s.removed, ip)
			s.mu.Unlock()
		}
	}
	if len(cams) == 0 {
		return
	}
	if !hasSite {
		s.once("site", now, func() {
			log.Printf("[SUN] No Site Location in the vehicle settings: the cameras' day/night times are not set")
		})
		return
	}
	for _, sw := range cams {
		rise, _ := sunTimes(now.AddDate(0, 0, 1), lat, lon) // tomorrow's sunrise: the next switch to day
		_, set := sunTimes(now, lat, lon)                   // today's sunset
		day := rise.Add(-time.Duration(sw.DayBefore) * time.Minute).Format("15:04")
		night := set.Add(time.Duration(sw.NightAfter) * time.Minute).Format("15:04")
		key := now.Format("2006-01-02") + " " + day + " " + night
		s.mu.Lock()
		done := s.applied[sw.Camera] == key
		s.mu.Unlock()
		if done {
			continue
		}
		sun := fmt.Sprintf("sunrise %s, sunset %s", rise.Format("15:04"), set.Format("15:04"))
		if s.set(sw.Camera, "time", day, night, env, sun) {
			s.mu.Lock()
			s.applied[sw.Camera] = key
			s.mu.Unlock()
		}
	}
}

// once runs f at most once a day for a key (a message that would repeat every check).
func (s *SunSwitcher) once(key string, now time.Time, f func()) {
	s.mu.Lock()
	day := now.Format("2006-01-02")
	seen := s.applied["#"+key] == day
	s.applied["#"+key] = day
	s.mu.Unlock()
	if !seen {
		f()
	}
}

var (
	sunProgramRe = regexp.MustCompile(`<program[^>]*>([a-z]+)</program>`)
	sunDayRe     = regexp.MustCompile(`<dayTime[^>]*><!\[CDATA\[([0-9:]+)\]\]></dayTime>`)
	sunNightRe   = regexp.MustCompile(`<nightTime[^>]*><!\[CDATA\[([0-9:]+)\]\]></nightTime>`)
)

// set sets a camera's switch (program "time" or "auto", and the times) and reads it back,
// only on production; it journals "sun_switch" either way, and reports whether it is done (on
// another server: journaled).
func (s *SunSwitcher) set(ip, program, day, night, env, sun string) bool {
	entry := map[string]interface{}{"camera_ip": ip, "program": program, "day": day, "night": night}
	if sun != "" {
		entry["sun"] = sun
	}
	if env != "production" {
		entry["skipped"] = fmt.Sprintf("environment is %q: only production changes the cameras", env)
		s.journal.Write("sun_switch", entry)
		return true
	}
	err := s.write(ip, program, day, night)
	if err != nil {
		entry["error"] = err.Error()
		log.Printf("[SUN] %s: could not set %s %s/%s: %v", ip, program, day, night, err)
	} else {
		entry["status"] = "set"
		log.Printf("[SUN] %s: day/night by %s, day %s, night %s (%s)", ip, program, day, night, sun)
	}
	s.journal.Write("sun_switch", entry)
	return err == nil
}

// write sends SetImageConfigFileInfo with the cameras' login and checks the read-back.
func (s *SunSwitcher) write(ip, program, day, night string) error {
	user, password, err := readCameraLogin(s.loginFile, specoLoginLabel)
	if err != nil {
		return err
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<config version="1.7" xmlns="http://www.ipc.com/ver10">` + "\n" +
		`<types><progType><enum>normal</enum><enum>time</enum><enum>auto</enum></progType></types>` + "\n" +
		`<program type="progType">` + program + `</program>` + "\n" +
		`<dayTime type="string"><![CDATA[` + day + `]]></dayTime>` + "\n" +
		`<nightTime type="string"><![CDATA[` + night + `]]></nightTime>` + "\n" +
		`</config>`
	r, err := s.post(ip, "SetImageConfigFileInfo", body, user, password)
	if err != nil {
		return err
	}
	if !strings.Contains(r, `status="success"`) {
		return fmt.Errorf("the camera answered: %.200s", r)
	}
	r, err = s.post(ip, "GetImageConfigFileInfo", "", user, password)
	if err != nil {
		return fmt.Errorf("reading it back: %w", err)
	}
	got := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(r); m != nil {
			return m[1]
		}
		return ""
	}
	if p, d, n := got(sunProgramRe), got(sunDayRe), got(sunNightRe); p != program || d != day || n != night {
		return fmt.Errorf("read back %s %s/%s, not %s %s/%s", p, d, n, program, day, night)
	}
	return nil
}

func (s *SunSwitcher) post(ip, cmd, body, user, password string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sunSwitchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base(ip)+"/"+cmd, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/xml")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", cmd, resp.StatusCode)
	}
	return string(data), nil
}

// sunTimes returns the day's sunrise and sunset at a latitude and longitude (degrees, north
// and east positive), in local time, by NOAA's approximate formulas (within a minute or two).
func sunTimes(day time.Time, lat, lon float64) (time.Time, time.Time) {
	y, m, d := day.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	event := func(rising bool) time.Time {
		hour := 18.0
		if rising {
			hour = 6
		}
		g := 2 * math.Pi / 365 * (float64(day.YearDay()-1) + (hour-lon/15)/24)
		eq := 229.18 * (0.000075 + 0.001868*math.Cos(g) - 0.032077*math.Sin(g) - 0.014615*math.Cos(2*g) - 0.040849*math.Sin(2*g))
		dec := 0.006918 - 0.399912*math.Cos(g) + 0.070257*math.Sin(g) - 0.006758*math.Cos(2*g) + 0.000907*math.Sin(2*g) - 0.002697*math.Cos(3*g) + 0.00148*math.Sin(3*g)
		rad := math.Pi / 180
		ha := math.Acos(math.Cos(90.833*rad)/(math.Cos(lat*rad)*math.Cos(dec))-math.Tan(lat*rad)*math.Tan(dec)) / rad
		if !rising {
			ha = -ha
		}
		minutes := 720 - 4*(lon+ha) - eq
		return midnight.Add(time.Duration(minutes * float64(time.Minute))).In(time.Local)
	}
	return event(true), event(false)
}
