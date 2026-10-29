package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Recorded video from the NVR, for the live monitor's Video button (CLAUDE.md, "Video from
// the NVR"). The NVR (a Speco N64NR, TVT's NVMS-9000 firmware) records every camera; the
// monitor asks WordPress, which asks this service on /video (this machine only), which plays
// the NVR's recording of one camera from a given time and sends it on as H.264 in fragmented
// MP4, which every browser plays while it arrives. Only reads the NVR: playback and its list
// of cameras.
//
//   - Login: the NVR URL, user and password are vehicle settings (nvr_url, nvr_user,
//     nvr_password). The NVR locks a user after 5 failed logins, so after one fails no other
//     is tried for nvrLoginBackoff.
//   - Channels: each camera's channel is found by its address in the NVR's list of cameras
//     (web API queryDevList: a devID of {0000002E-...} is channel 46), kept nvrChannelsFor.
//   - RTSP: Digest, with the password sent as its MD5 in upper-case hex, as the NVR's own web
//     page sends it (the plain password is refused). Playback is
//     chID=<n>&date=YYYY-MM-DD&time=HH:MM:SS&timelen=<s>&streamType=main|sub&linkType=tcp&action=playback,
//     in local time, and arrives at normal speed. The NVR stops sending about 6 s before the
//     length asked for, so it is asked for nvrExtraLength more and the clip is cut here.
//   - Always the main stream: the NVR sends most cameras' second stream only after about 17 s
//     and then all at once, the main stream after 1 to 13 s (2026-10-25, the lane cameras).
//   - Most cameras record H.265, which browsers play unreliably, so ffmpeg converts to H.264
//     at most 720 lines high, or 1080 with quality=sharp, in real time on the testbed
//     (2026-10-25: 4K and 1080p).
//   - The NVR user needs remote live view and playback for each lane camera's channel (the
//     NVR answers 404 "Stream Not Found" otherwise).
const (
	nvrRTSPPort     = 554
	nvrExtraLength  = 15 * time.Second
	nvrChannelsFor  = 10 * time.Minute
	nvrLoginBackoff = 5 * time.Minute
	nvrMaxStreams   = 6 // ffmpeg conversions at once
	// A clip's conversion ends this long after its length, whatever the viewer does. A browser
	// that stops reading (its buffer full, or the player closed) keeps the connection open,
	// and without a limit the conversion waited on it for minutes (2026-10-25).
	nvrStreamGrace   = 45 * time.Second
	nvrMaxClipLength = 10 * time.Minute
	nvrTimestamp     = "2006-01-02 15:04:05"
)

type NVRConfig struct {
	URL, User, Password string
}

type NVRVideo struct {
	cfg     atomic.Pointer[NVRConfig]
	streams chan struct{}

	mu         sync.Mutex
	channels   map[string]int // camera address -> channel
	channelsAt time.Time
	failedAt   time.Time // the last failed login
	failedFor  NVRConfig // with these settings; new settings may try at once
}

func NewNVRVideo() *NVRVideo {
	return &NVRVideo{streams: make(chan struct{}, nvrMaxStreams)}
}

func (v *NVRVideo) SetConfig(c *ServiceConfig) {
	v.cfg.Store(&NVRConfig{URL: strings.TrimRight(strings.TrimSpace(c.NVRURL), "/"), User: c.NVRUser, Password: c.NVRPassword})
}

// passwordHash is how the NVR wants the password, on the web login and in RTSP's Digest.
func nvrPasswordHash(password string) string {
	sum := md5.Sum([]byte(password))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

var (
	nvrNonceRE   = regexp.MustCompile(`<nonce>(.*?)</nonce>`)
	nvrTokenRE   = regexp.MustCompile(`<token>(.*?)</token>`)
	nvrSessionRE = regexp.MustCompile(`<sessionId>\{?(.*?)\}?</sessionId>`)
	nvrItemRE    = regexp.MustCompile(`(?s)<devID>\{([0-9A-Fa-f]{8})-.*?</devID>.*?<ip>([^<]*)</ip>`)
)

// nvrSession is one web API login.
type nvrSession struct {
	base, token, sid string
	client           *http.Client
}

func (s *nvrSession) post(ctx context.Context, cmd, body string) (string, error) {
	data := `<?xml version="1.0" encoding="utf-8" ?><request version="1.0" systemType="NVMS-9000" clientType="WEB"><token>` + s.token + `</token>` + body + `</request>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/"+cmd, strings.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/xml")
	if s.sid != "" {
		req.Header.Set("Cookie", "sessionId="+s.sid)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

func nvrStatusOK(r string) bool { return strings.Contains(r, "<status>success</status>") }

// loadChannels logs in to the NVR's web API, reads its list of cameras and logs out.
func (v *NVRVideo) loadChannels(ctx context.Context, c NVRConfig) (map[string]int, error) {
	s := &nvrSession{base: c.URL, token: "null", client: &http.Client{Timeout: 10 * time.Second}}
	r, err := s.post(ctx, "reqLogin", "")
	if err != nil {
		return nil, fmt.Errorf("NVR not reached: %w", err)
	}
	nonce, token, sid := nvrNonceRE.FindStringSubmatch(r), nvrTokenRE.FindStringSubmatch(r), nvrSessionRE.FindStringSubmatch(r)
	if !nvrStatusOK(r) || nonce == nil || token == nil || sid == nil {
		return nil, errors.New("NVR refused the login request (is the NVR URL right?)")
	}
	s.token, s.sid = token[1], sid[1]
	h := sha512.Sum512([]byte(nvrPasswordHash(c.Password) + "#" + nonce[1]))
	r, err = s.post(ctx, "doLogin", "<content><userName><![CDATA["+c.User+"]]></userName><password><![CDATA["+hex.EncodeToString(h[:])+"]]></password></content>")
	if err != nil {
		return nil, fmt.Errorf("NVR login: %w", err)
	}
	if !nvrStatusOK(r) {
		v.mu.Lock()
		v.failedAt, v.failedFor = time.Now(), c
		v.mu.Unlock()
		return nil, fmt.Errorf("the NVR refused the user %q and its password; no other login is tried for %v (the NVR locks a user after 5 failures)", c.User, nvrLoginBackoff)
	}
	defer s.post(context.Background(), "doLogout", "")
	r, err = s.post(ctx, "queryDevList", "")
	if err != nil || !nvrStatusOK(r) {
		return nil, fmt.Errorf("NVR camera list: %v", err)
	}
	channels := map[string]int{}
	for _, m := range nvrItemRE.FindAllStringSubmatch(r, -1) {
		if n, err := strconv.ParseInt(m[1], 16, 32); err == nil {
			channels[strings.TrimSpace(m[2])] = int(n)
		}
	}
	return channels, nil
}

// channel is the NVR channel recording the camera at ip.
func (v *NVRVideo) channel(ctx context.Context, c NVRConfig, ip string) (int, error) {
	v.mu.Lock()
	if v.channels != nil && time.Since(v.channelsAt) < nvrChannelsFor {
		if n, ok := v.channels[ip]; ok {
			v.mu.Unlock()
			return n, nil
		}
	}
	if !v.failedAt.IsZero() && v.failedFor == c && time.Since(v.failedAt) < nvrLoginBackoff {
		v.mu.Unlock()
		return 0, fmt.Errorf("the NVR refused the login %v ago; waiting before trying again (check the NVR user and password)", time.Since(v.failedAt).Round(time.Second))
	}
	v.mu.Unlock()
	channels, err := v.loadChannels(ctx, c)
	if err != nil {
		return 0, err
	}
	v.mu.Lock()
	v.channels, v.channelsAt = channels, time.Now()
	v.mu.Unlock()
	n, ok := channels[ip]
	if !ok {
		return 0, fmt.Errorf("the NVR records no camera at %s", ip)
	}
	return n, nil
}

// nvrPlayback is the RTSP address of channel n's recording from start, for length.
func nvrPlayback(c NVRConfig, host string, n int, start time.Time, length time.Duration) string {
	return fmt.Sprintf("rtsp://%s:%s@%s:%d/chID=%d&date=%s&time=%s&timelen=%d&streamType=main&linkType=tcp&action=playback",
		url.QueryEscape(c.User), nvrPasswordHash(c.Password), host, nvrRTSPPort, n,
		start.Format("2006-01-02"), start.Format("15:04:05"), int((length+nvrExtraLength)/time.Second))
}

// Frames saves the camera's recorded frames from start, for length, fps a second, at full size,
// as dir/f001.jpg, f002.jpg and on (front_plate.go). It waits for one of the nvrMaxStreams.
func (v *NVRVideo) Frames(ctx context.Context, ip string, start time.Time, length time.Duration, fps int, dir string) error {
	c := v.cfg.Load()
	if c == nil || c.URL == "" || c.User == "" || c.Password == "" {
		return errors.New("no NVR set in the vehicle settings")
	}
	nvr, err := url.Parse(c.URL)
	if err != nil || nvr.Hostname() == "" {
		return errors.New("the NVR URL in the vehicle settings isn't a URL")
	}
	n, err := v.channel(ctx, *c, ip)
	if err != nil {
		return err
	}
	select {
	case v.streams <- struct{}{}:
		defer func() { <-v.streams }()
	case <-ctx.Done():
		return ctx.Err()
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-y",
		"-rtsp_transport", "tcp", "-timeout", "10000000", "-i", nvrPlayback(*c, nvr.Hostname(), n, start, length),
		"-t", strconv.FormatFloat(length.Seconds(), 'f', 1, 64), "-an", "-vf", fmt.Sprintf("fps=%d", fps), "-q:v", "2",
		dir+"/f%03d.jpg")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		hash := nvrPasswordHash(c.Password)
		// ffmpeg's messages can hold the address with the login: never pass them on unmasked
		msg := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(stderr.String(), hash, "***"), url.QueryEscape(c.User)+":", "***:"))
		return fmt.Errorf("channel %d: %v %.200s", n, err, msg)
	}
	return nil
}

// ServeHTTP is /video?camera=<address>&at=<YYYY-MM-DD HH:MM:SS>&before=<s>&length=<s>&quality=sharp:
// the camera's recording from `before` seconds before `at`, for `length` seconds (defaults 15
// and 60). The camera must be one of the lanes'. This machine only (WordPress passes it on).
func (v *NVRVideo) ServeHTTP(w http.ResponseWriter, r *http.Request, gates *Gates) {
	if addr, ok := remoteAddr(r.RemoteAddr); !ok || !addr.IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	c := v.cfg.Load()
	if c == nil || c.URL == "" || c.User == "" || c.Password == "" {
		http.Error(w, "no NVR set in the vehicle settings", http.StatusServiceUnavailable)
		return
	}
	nvr, err := url.Parse(c.URL)
	if err != nil || nvr.Hostname() == "" {
		http.Error(w, "the NVR URL in the vehicle settings isn't a URL", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	ip := strings.TrimSpace(q.Get("camera"))
	if _, ok := gates.ForIP(ip); !ok {
		http.Error(w, "not a lane camera", http.StatusBadRequest)
		return
	}
	atText := strings.TrimSpace(q.Get("at"))
	atText = atText[:min(len(atText), len(nvrTimestamp))] // event_timestamp may have milliseconds
	at, err := time.ParseInLocation(nvrTimestamp, atText, time.Local)
	if err != nil {
		http.Error(w, "at must be YYYY-MM-DD HH:MM:SS", http.StatusBadRequest)
		return
	}
	seconds := func(name string, def, lo, hi time.Duration) time.Duration {
		f, err := strconv.ParseFloat(q.Get(name), 64)
		if err != nil {
			return def
		}
		return min(max(time.Duration(f*float64(time.Second)), lo), hi)
	}
	start := at.Add(-seconds("before", 15*time.Second, -nvrMaxClipLength, 24*time.Hour))
	length := seconds("length", time.Minute, 5*time.Second, nvrMaxClipLength)
	height := 720
	if q.Get("quality") == "sharp" {
		height = 1080
	}

	n, err := v.channel(r.Context(), *c, ip)
	if err != nil {
		log.Printf("[NVR] %s: %v", ip, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	select {
	case v.streams <- struct{}{}:
		defer func() { <-v.streams }()
	default:
		http.Error(w, fmt.Sprintf("already playing %d videos; close one and try again", nvrMaxStreams), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), length+nvrStreamGrace)
	defer cancel()
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(length + nvrStreamGrace))

	hash := nvrPasswordHash(c.Password)
	src := nvrPlayback(*c, nvr.Hostname(), n, start, length)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-timeout", "10000000", "-i", src,
		"-t", strconv.Itoa(int(length/time.Second)), "-an",
		"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)'", height), "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-g", "20", "-pix_fmt", "yuv420p",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, "ffmpeg: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[NVR] Playing %s (channel %d, %dp) from %s for %v", ip, n, height, start.Format(nvrTimestamp), length)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	sent := 0
	for {
		k, rerr := out.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				break // the viewer went away: CommandContext stops ffmpeg
			}
			sent += k
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	err = cmd.Wait()
	// ffmpeg's messages can hold the address with the login: never log them unmasked
	msg := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(stderr.String(), hash, "***"), url.QueryEscape(c.User)+":", "***:"))
	if r.Context().Err() == nil && (err != nil || msg != "") {
		log.Printf("[NVR] %s: %d bytes sent; ffmpeg: %v %.300s", ip, sent, err, msg)
	}
	if sent == 0 && r.Context().Err() == nil {
		reason := "the NVR sent no video for that time"
		if strings.Contains(msg, "404") {
			reason = fmt.Sprintf("the NVR refused channel %d: give the NVR user remote live view and playback for it", n)
		}
		http.Error(w, reason, http.StatusBadGateway)
	}
}
