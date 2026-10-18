// Package cast discovers Chromecasts and plays library items on them using a
// small CASTv2 client (the go-chromecast library cannot send subtitle tracks).
package cast

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"homemedia/internal/model"
	"homemedia/internal/probe"
)

// Device is a Chromecast found on the network.
type Device struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
}

// Library is what the cast manager needs from internal/library.
type Library interface {
	Get(id string) (model.Item, bool)
	SetProgress(id string, pos, dur float64)
	Progress(id string) (model.Progress, bool)
}

// minResume is the shortest saved position (seconds) worth asking about.
const minResume = 30

// Manager owns device discovery and one session per device.
type Manager struct {
	lib  Library
	base string // http://<lan>:<port>, how the Chromecast reaches this server
	// StopHook is called when playback of an item ends (finished or stopped) so
	// transcoding can be stopped. May be nil.
	StopHook func(itemID string)

	mu       sync.Mutex
	devices  map[string]*deviceEntry
	sessions map[string]*session
}

type deviceEntry struct {
	Device
	seen time.Time
}

// NewManager creates a manager. base must use the LAN address, never localhost.
func NewManager(lib Library, base string) *Manager {
	return &Manager{lib: lib, base: strings.TrimRight(base, "/"), devices: map[string]*deviceEntry{}, sessions: map[string]*session{}}
}

func (m *Manager) onStop(itemID string) {
	if m.StopHook != nil {
		m.StopHook(itemID)
	}
}

// AddDevice registers a device manually (tests, or a static address).
func (m *Manager) AddDevice(d Device) {
	m.mu.Lock()
	m.devices[d.ID] = &deviceEntry{Device: d, seen: time.Now().Add(100 * 365 * 24 * time.Hour)}
	m.mu.Unlock()
}

// Devices lists devices seen in the last few minutes, by name.
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, e := range m.devices {
		if time.Since(e.seen) < 3*time.Minute {
			out = append(out, e.Device)
		}
	}
	sortDevices(out)
	return out
}

func sortDevices(d []Device) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && strings.ToLower(d[j].Name) < strings.ToLower(d[j-1].Name); j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

// Run browses for Chromecasts until ctx is cancelled, then closes all sessions.
func (m *Manager) Run(ctx context.Context) {
	defer func() {
		m.mu.Lock()
		for _, s := range m.sessions {
			s.close()
		}
		m.mu.Unlock()
	}()
	for {
		if err := m.browse(ctx, 6*time.Second); err != nil && ctx.Err() == nil {
			slog.Warn("chromecast discovery failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
	}
}

func (m *Manager) browse(ctx context.Context, d time.Duration) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry)
	bctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range entries {
			if dev, ok := deviceFromEntry(e); ok {
				m.mu.Lock()
				if old := m.devices[dev.ID]; old == nil {
					slog.Info("chromecast found", "name", dev.Name, "model", dev.Model, "addr", dev.Addr)
				}
				m.devices[dev.ID] = &deviceEntry{Device: dev, seen: time.Now()}
				m.mu.Unlock()
			}
		}
	}()
	err = resolver.Browse(bctx, "_googlecast._tcp", "local.", entries)
	<-bctx.Done()
	<-done
	return err
}

func deviceFromEntry(e *zeroconf.ServiceEntry) (Device, bool) {
	txt := map[string]string{}
	for _, t := range e.Text {
		if k, v, ok := strings.Cut(t, "="); ok {
			txt[k] = v
		}
	}
	var addr string
	for _, ip := range e.AddrIPv4 {
		addr = ip.String()
		break
	}
	if addr == "" {
		for _, ip := range e.AddrIPv6 {
			if !ip.IsLinkLocalUnicast() {
				addr = ip.String()
				break
			}
		}
	}
	if addr == "" || txt["id"] == "" {
		return Device{}, false
	}
	// "ca" is a capability bitmask; bit 0 is video out. Speakers lack it.
	if ca, err := strconv.Atoi(txt["ca"]); err == nil && ca&1 == 0 {
		return Device{}, false
	}
	name := txt["fn"]
	if name == "" {
		name = e.Instance
	}
	return Device{ID: txt["id"], Name: name, Model: txt["md"], Addr: addr, Port: e.Port}, true
}

// resolve finds a device by ID or (case-insensitive) friendly name.
func (m *Manager) resolve(key string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.devices[key]; ok {
		return e.Device, nil
	}
	for _, e := range m.devices {
		if strings.EqualFold(e.Name, key) {
			return e.Device, nil
		}
	}
	return Device{}, fmt.Errorf("%w %q", ErrNoDevice, key)
}

func (m *Manager) sessionFor(dev Device) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[dev.ID]
	if s == nil {
		s = newSession(m, dev)
		m.sessions[dev.ID] = s
	} else if s.dev.Addr != dev.Addr || s.dev.Port != dev.Port {
		s.dev = dev // the device changed address; the next connect uses it
	}
	return s
}

// PlayRequest starts an item on a device.
type PlayRequest struct {
	ItemID string  `json:"id"`
	Audio  int     `json:"audio"`
	Sub    string  `json:"sub"`   // sub track id, "" = none
	Start  float64 `json:"start"` // seconds; > 0 skips the resume question
	Mode   string  `json:"mode"`  // auto | direct | transcode
	// Resume answers the "continue or restart?" question for a partly watched
	// item: "continue" starts at the saved position, "restart" at 0. Empty asks
	// (a *ResumeError) when there is a saved position and Start is not given.
	Resume string `json:"resume"`
}

// ResumeError tells the caller the item was partly watched and needs a choice.
type ResumeError struct {
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
}

func (e *ResumeError) Error() string {
	return fmt.Sprintf("already watched up to %s: choose resume=continue or resume=restart", clock(e.Position))
}

// clock formats seconds as h:mm:ss or m:ss.
func clock(sec float64) string {
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// loadSpec is a fully built LOAD plus what the session must remember about it.
type loadSpec struct {
	payload   map[string]any
	trackSubs map[int]string
	audio     int
	activeSub string
	mode      string
	start     float64
	duration  float64
}

// BuildLoad builds the LOAD message for item on a device model. Exported for tests.
func (m *Manager) BuildLoad(item model.Item, deviceModel string, req PlayRequest) (loadSpec, error) {
	if item.Info == nil || item.Info.Video == nil {
		return loadSpec{}, badRequest("item has no video")
	}
	mode := req.Mode
	direct := probe.CanDirectPlay(item.Path, item.Info, probe.CapsForModel(deviceModel))
	switch mode {
	case "direct":
		if !direct {
			return loadSpec{}, badRequest("this file cannot be played directly on this device")
		}
	case "transcode":
		direct = false
	default:
		mode = "auto"
	}
	var url, ctype string
	if direct {
		url, ctype, mode = fmt.Sprintf("%s/file/%s", m.base, item.ID), contentTypeForFile(item.Path), "direct"
	} else {
		if req.Audio < 0 || (len(item.Info.Audio) > 0 && req.Audio >= len(item.Info.Audio)) {
			return loadSpec{}, badRequest("audio track %d does not exist", req.Audio)
		}
		url, ctype, mode = fmt.Sprintf("%s/hls/%s/%d/index.m3u8", m.base, item.ID, req.Audio), "application/x-mpegURL", "transcode"
	}

	spec := loadSpec{trackSubs: map[int]string{}, audio: req.Audio, mode: mode, start: req.Start, duration: item.Info.Duration}
	if direct {
		spec.audio = 0
	}
	tracks := []map[string]any{}
	active := []int{}
	for i, sub := range item.Subs {
		tid := i + 1
		spec.trackSubs[tid] = sub.ID
		t := map[string]any{
			"trackId": tid, "type": "TEXT", "subtype": "SUBTITLES",
			"trackContentId":   fmt.Sprintf("%s/sub/%s/%s.vtt", m.base, item.ID, sub.ID),
			"trackContentType": "text/vtt", "name": sub.Label,
		}
		if sub.Lang != "" {
			t["language"] = sub.Lang
		}
		tracks = append(tracks, t)
		if sub.ID == req.Sub {
			active = append(active, tid)
			spec.activeSub = sub.ID
		}
	}
	if req.Sub != "" && spec.activeSub == "" {
		return loadSpec{}, badRequest("unknown subtitle %q", req.Sub)
	}

	meta := map[string]any{"metadataType": 0, "title": titleFor(item)}
	if item.Art.Backdrop {
		meta["images"] = []map[string]any{{"url": fmt.Sprintf("%s/art/%s/backdrop", m.base, item.ID)}}
	}
	media := map[string]any{
		"contentId": url, "contentType": ctype, "streamType": "BUFFERED",
		"duration": item.Info.Duration, "metadata": meta, "tracks": tracks,
		"textTrackStyle": map[string]any{
			"foregroundColor": "#FFFFFFFF", "backgroundColor": "#00000000",
			"edgeType": "OUTLINE", "edgeColor": "#000000FF", "fontScale": 1.0,
		},
	}
	spec.payload = map[string]any{
		"type": "LOAD", "autoplay": true, "currentTime": req.Start,
		"media": media, "activeTrackIds": active,
	}
	return spec, nil
}

func titleFor(item model.Item) string {
	p := item.Parsed
	if item.Meta != nil && item.Meta.Title != "" && p.Type == model.TypeMovie {
		return item.Meta.Title
	}
	if p.Type == model.TypeEpisode {
		t := fmt.Sprintf("%s S%02dE%02d", p.Show, p.Season, p.Episode)
		if item.Meta != nil && item.Meta.Title != "" {
			t += " " + item.Meta.Title
		}
		return t
	}
	return p.Title
}

// Play loads an item on a device (connecting and launching the receiver as needed).
func (m *Manager) Play(ctx context.Context, deviceKey string, req PlayRequest) error {
	dev, err := m.resolve(deviceKey)
	if err != nil {
		return err
	}
	item, ok := m.lib.Get(req.ItemID)
	if !ok {
		return badRequest("unknown item %q", req.ItemID)
	}
	if req.Start < 0 {
		req.Start = 0
	}
	switch req.Resume {
	case "", "continue", "restart":
	default:
		return badRequest("resume must be continue or restart")
	}
	if req.Start == 0 && req.Resume != "restart" {
		if pr, ok := m.lib.Progress(item.ID); ok && !pr.Watched && pr.Pos >= minResume {
			if req.Resume == "" {
				return &ResumeError{Position: float64(pr.Pos), Duration: pr.Dur}
			}
			req.Start = float64(pr.Pos)
		}
	}
	spec, err := m.BuildLoad(item, dev.Model, req)
	if err != nil {
		return err
	}
	return m.sessionFor(dev).play(ctx, item, spec)
}

// Control sends a transport command to a device.
func (m *Manager) Control(ctx context.Context, deviceKey string, req ControlRequest) error {
	dev, err := m.resolve(deviceKey)
	if err != nil {
		return err
	}
	return m.sessionFor(dev).control(ctx, req)
}

// Status reports a device's playback state without connecting.
func (m *Manager) Status(deviceKey string) (Status, error) {
	dev, err := m.resolve(deviceKey)
	if err != nil {
		return Status{}, err
	}
	return m.sessionFor(dev).status(), nil
}

// ErrNoDevice is returned (wrapped) when a device lookup fails, so the HTTP layer can answer 404.
var ErrNoDevice = errors.New("unknown device")

var errBadRequest = errors.New("bad request")

// badRequest marks an error caused by the caller's input (HTTP 400).
func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}
