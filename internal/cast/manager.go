// Package cast discovers Chromecasts and plays library items on them using a
// small CASTv2 client (the go-chromecast library cannot send subtitle tracks).
package cast

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"homemedia/internal/model"
	"homemedia/internal/probe"
)

// Device is a Chromecast found on the network.
type Device = model.Device

// Library is what the cast manager needs from internal/library.
type Library interface {
	Get(id string) (model.Item, bool)
	SetProgress(id string, pos, dur float64)
	Progress(id string) (model.Progress, bool)
}

const (
	// minResume is the shortest saved position (seconds) worth asking about.
	minResume = 30
	// deviceTTL is how long a device stays listed after it was last seen.
	deviceTTL = 3 * time.Minute
	// pinnedTTL keeps manually added devices listed for good.
	pinnedTTL = 100 * 365 * 24 * time.Hour
	// browseFor and browseEvery set how long each mDNS browse runs and the pause between them.
	browseFor   = 6 * time.Second
	browseEvery = 20 * time.Second
	// directContentType is sent for direct play; only MP4-family files play directly
	// (see probe.castVideoContainers).
	directContentType = "video/mp4"
)

// Manager owns device discovery and one session per device.
type Manager struct {
	lib Library
	// StopHook is called when playback of an item ends (finished or stopped) so
	// transcoding can be stopped. May be nil.
	StopHook func(itemID string)

	mu       sync.Mutex
	base     string // http://<lan>:<port>, how the Chromecast reaches this server; guarded by mu
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

// SetBase changes the URL Chromecasts use to reach this server, for when the
// machine's LAN address changes while mediad runs.
func (m *Manager) SetBase(base string) {
	m.mu.Lock()
	m.base = strings.TrimRight(base, "/")
	m.mu.Unlock()
}

func (m *Manager) baseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base
}

func (m *Manager) onStop(itemID string) {
	if m.StopHook != nil {
		m.StopHook(itemID)
	}
}

// AddDevice registers a device manually (tests, or a static address).
func (m *Manager) AddDevice(d Device) {
	m.mu.Lock()
	m.devices[d.ID] = &deviceEntry{Device: d, seen: time.Now().Add(pinnedTTL)}
	m.mu.Unlock()
}

// Devices lists devices seen in the last few minutes, by name.
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, e := range m.devices {
		if time.Since(e.seen) < deviceTTL {
			out = append(out, e.Device)
		}
	}
	slices.SortStableFunc(out, func(a, b Device) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
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
		if err := m.browse(ctx, browseFor); err != nil && ctx.Err() == nil {
			slog.Warn("chromecast discovery failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(browseEvery):
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
	} else {
		s.mu.Lock()
		if s.dev.Addr != dev.Addr || s.dev.Port != dev.Port {
			s.dev = dev // the device changed address; the next connect uses it
		}
		s.mu.Unlock()
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
	return fmt.Sprintf("already watched up to %s: choose resume=continue or resume=restart", model.Clock(e.Position))
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

// buildLoad builds the LOAD message for item on a device model.
func (m *Manager) buildLoad(item model.Item, deviceModel string, req PlayRequest) (loadSpec, error) {
	if item.Info == nil || item.Info.Video == nil {
		return loadSpec{}, badRequest("item has no video")
	}
	base := m.baseURL()
	st, err := chooseStream(base, item, deviceModel, req)
	if err != nil {
		return loadSpec{}, err
	}
	tracks, trackSubs, active, err := subtitleTracks(base, item, req.Sub)
	if err != nil {
		return loadSpec{}, err
	}
	spec := loadSpec{trackSubs: trackSubs, audio: st.audio, mode: st.mode, start: req.Start, duration: item.Info.Duration}
	if len(active) > 0 {
		spec.activeSub = req.Sub
	}
	spec.payload = map[string]any{
		"type": "LOAD", "autoplay": true, "currentTime": req.Start,
		"media": mediaInfo(base, item, st, tracks), "activeTrackIds": active,
	}
	return spec, nil
}

// stream is where the receiver fetches the video from, and how.
type stream struct {
	url, contentType string
	mode             string // direct | transcode
	audio            int
}

// chooseStream picks direct play or HLS for req.Mode ("direct", "transcode",
// or anything else for whatever the device can play).
func chooseStream(base string, item model.Item, deviceModel string, req PlayRequest) (stream, error) {
	direct := probe.CanDirectPlay(item.Path, item.Info, probe.CapsForModel(deviceModel))
	switch req.Mode {
	case "direct":
		if !direct {
			return stream{}, badRequest("this file cannot be played directly on this device")
		}
	case "transcode":
		direct = false
	}
	if direct {
		return stream{url: fmt.Sprintf("%s/file/%s", base, item.ID), contentType: directContentType, mode: "direct"}, nil
	}
	if req.Audio < 0 || (len(item.Info.Audio) > 0 && req.Audio >= len(item.Info.Audio)) {
		return stream{}, badRequest("audio track %d does not exist", req.Audio)
	}
	return stream{
		url:         fmt.Sprintf("%s/hls/%s/%d/index.m3u8", base, item.ID, req.Audio),
		contentType: "application/x-mpegURL", mode: "transcode", audio: req.Audio,
	}, nil
}

// subtitleTracks lists item's subtitles as Cast text tracks, maps each track
// ID back to its sub ID, and returns the track ID of sub (none when sub is "").
func subtitleTracks(base string, item model.Item, sub string) (tracks []map[string]any, trackSubs map[int]string, active []int, err error) {
	tracks, trackSubs, active = []map[string]any{}, map[int]string{}, []int{}
	for i, s := range item.Subs {
		tid := i + 1
		trackSubs[tid] = s.ID
		t := map[string]any{
			"trackId": tid, "type": "TEXT", "subtype": "SUBTITLES",
			"trackContentId":   fmt.Sprintf("%s/sub/%s/%s.vtt", base, item.ID, s.ID),
			"trackContentType": "text/vtt", "name": s.Label,
		}
		if s.Lang != "" {
			t["language"] = s.Lang
		}
		tracks = append(tracks, t)
		if s.ID == sub {
			active = append(active, tid)
		}
	}
	if sub != "" && len(active) == 0 {
		return nil, nil, nil, badRequest("unknown subtitle %q", sub)
	}
	return tracks, trackSubs, active, nil
}

// mediaInfo is the LOAD message's media object.
func mediaInfo(base string, item model.Item, st stream, tracks []map[string]any) map[string]any {
	meta := map[string]any{"metadataType": 0, "title": titleFor(item)}
	if item.Art.Backdrop {
		meta["images"] = []map[string]any{{"url": fmt.Sprintf("%s/art/%s/backdrop", base, item.ID)}}
	}
	return map[string]any{
		"contentId": st.url, "contentType": st.contentType, "streamType": "BUFFERED",
		"duration": item.Info.Duration, "metadata": meta, "tracks": tracks,
		"textTrackStyle": map[string]any{
			"foregroundColor": "#FFFFFFFF", "backgroundColor": "#00000000",
			"edgeType": "OUTLINE", "edgeColor": "#000000FF", "fontScale": 1.0,
		},
	}
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
	spec, err := m.buildLoad(item, dev.Model, req)
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
