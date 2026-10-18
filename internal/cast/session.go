package cast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"homemedia/internal/model"
)

// defaultReceiverApp is Google's Default Media Receiver; it needs no registration.
const defaultReceiverApp = "CC1AD845"

const (
	heartbeatEvery = 5 * time.Second
	deadAfter      = 20 * time.Second
	saveEvery      = 10 * time.Second
	loadTimeout    = 40 * time.Second // HLS has to transcode its first segment
)

// Status is what clients see for a device.
type Status struct {
	State      string  `json:"state"` // DISCONNECTED | IDLE | LOADING | PLAYING | PAUSED | BUFFERING
	Position   float64 `json:"position"`
	Duration   float64 `json:"duration"`
	ItemID     string  `json:"itemId"`
	Audio      int     `json:"audio"`
	Sub        string  `json:"sub"` // sub track id, "" = off
	Mode       string  `json:"mode"`
	Volume     float64 `json:"volume"`
	Muted      bool    `json:"muted"`
	IdleReason string  `json:"idleReason,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type session struct {
	m   *Manager
	dev Device

	mu          sync.Mutex
	c           *conn
	changed     chan struct{} // closed and replaced on every state change
	transportID string
	sessionID   string
	mediaID     int // mediaSessionId of the current LOAD, 0 = none
	st          Status
	stAt        time.Time // when st.Position was last reported
	lastRx      time.Time
	lastSave    time.Time
	tracks      map[int]string // castTrackId -> sub id
	loadReq     int            // requestId of the in-flight LOAD
	loadErr     string
}

func newSession(m *Manager, dev Device) *session {
	return &session{m: m, dev: dev, changed: make(chan struct{}), st: Status{State: "DISCONNECTED"}}
}

func (s *session) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// waitFor blocks until cond (called with s.mu held) is true.
func (s *session) waitFor(ctx context.Context, timeout time.Duration, cond func() bool) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		s.mu.Lock()
		if cond() {
			s.mu.Unlock()
			return nil
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-t.C:
			return errors.New("timed out waiting for the chromecast")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ensure connects (if needed) and makes sure the Default Media Receiver runs.
func (s *session) ensure(ctx context.Context) error {
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c == nil {
		nc, err := dial(s.dev.Addr, s.dev.Port, 5*time.Second)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.c, s.lastRx = nc, time.Now()
		s.st.State = "IDLE"
		s.mu.Unlock()
		in := make(chan message, 16)
		go nc.readLoop(in)
		go s.dispatch(nc, in)
		go s.heartbeat(nc)
		if _, err := nc.send(nsConnection, receiverID, map[string]any{"type": "CONNECT"}); err != nil {
			s.drop(nc)
			return err
		}
	}
	return s.ensureApp(ctx)
}

func (s *session) hasApp() bool { return s.transportID != "" }

func (s *session) ensureApp(ctx context.Context) error {
	s.mu.Lock()
	c, ok := s.c, s.hasApp()
	s.mu.Unlock()
	if ok {
		return nil
	}
	if c == nil {
		return errClosed
	}
	// The receiver may already run it; ask, then launch if it does not show up.
	if _, err := c.send(nsReceiver, receiverID, map[string]any{"type": "GET_STATUS"}); err != nil {
		return err
	}
	if s.waitFor(ctx, 1500*time.Millisecond, s.hasApp) == nil {
		return nil
	}
	if _, err := c.send(nsReceiver, receiverID, map[string]any{"type": "LAUNCH", "appId": defaultReceiverApp}); err != nil {
		return err
	}
	return s.waitFor(ctx, 15*time.Second, s.hasApp)
}

// drop tears down c if it is still the current connection.
func (s *session) drop(c *conn) {
	_ = c.close()
	s.mu.Lock()
	if s.c == c {
		s.c, s.transportID, s.sessionID, s.mediaID = nil, "", "", 0
		s.st.State = "DISCONNECTED"
		s.notifyLocked()
	}
	s.mu.Unlock()
}

func (s *session) heartbeat(c *conn) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		cur := s.c == c
		dead := time.Since(s.lastRx) > deadAfter
		media, transport, playing := s.mediaID, s.transportID, s.st.State == "PLAYING" || s.st.State == "BUFFERING"
		s.mu.Unlock()
		if !cur {
			return
		}
		if dead {
			slog.Warn("chromecast stopped answering", "device", s.dev.Name)
			s.drop(c)
			return
		}
		if _, err := c.send(nsHeartbeat, receiverID, map[string]any{"type": "PING"}); err != nil {
			s.drop(c)
			return
		}
		// MEDIA_STATUS is only pushed on changes; poll while playing to keep progress fresh.
		if playing && media != 0 && transport != "" {
			_, _ = c.send(nsMedia, transport, map[string]any{"type": "GET_STATUS", "mediaSessionId": media})
		}
	}
}

func (s *session) dispatch(c *conn, in <-chan message) {
	for m := range in {
		s.handle(c, m)
	}
	s.drop(c)
}

type mediaStatus struct {
	MediaSessionID int     `json:"mediaSessionId"`
	PlayerState    string  `json:"playerState"`
	CurrentTime    float64 `json:"currentTime"`
	IdleReason     string  `json:"idleReason"`
	ActiveTrackIDs []int   `json:"activeTrackIds"`
	Media          *struct {
		Duration float64 `json:"duration"`
	} `json:"media"`
}

func (s *session) handle(c *conn, m message) {
	var h header
	if json.Unmarshal(m.Data, &h) != nil {
		return
	}
	s.mu.Lock()
	if s.c != c {
		s.mu.Unlock()
		return
	}
	s.lastRx = time.Now()
	var save *saveReq

	switch {
	case m.Namespace == nsReceiver && h.Type == "RECEIVER_STATUS":
		var p struct {
			Status struct {
				Applications []struct {
					AppID       string `json:"appId"`
					TransportID string `json:"transportId"`
					SessionID   string `json:"sessionId"`
				} `json:"applications"`
				Volume struct {
					Level float64 `json:"level"`
					Muted bool    `json:"muted"`
				} `json:"volume"`
			} `json:"status"`
		}
		if json.Unmarshal(m.Data, &p) == nil {
			s.st.Volume, s.st.Muted = p.Status.Volume.Level, p.Status.Volume.Muted
			found := false
			for _, a := range p.Status.Applications {
				if a.AppID != defaultReceiverApp {
					continue
				}
				found = true
				if s.transportID != a.TransportID {
					s.transportID, s.sessionID = a.TransportID, a.SessionID
					_, _ = c.send(nsConnection, a.TransportID, map[string]any{"type": "CONNECT"})
				}
			}
			if !found && s.transportID != "" {
				// Our app was closed (stopped from here, or by someone else).
				save = s.leaveLocked()
				s.transportID, s.sessionID, s.mediaID = "", "", 0
				s.st.State, s.st.Position, s.st.ItemID = "IDLE", 0, ""
			}
		}

	case m.Namespace == nsConnection && h.Type == "CLOSE":
		if m.Source != receiverID {
			save = s.leaveLocked()
			s.transportID, s.sessionID, s.mediaID = "", "", 0
			s.st.State, s.st.ItemID = "IDLE", ""
		}

	case m.Namespace == nsMedia && h.Type == "MEDIA_STATUS":
		var p struct {
			Status []mediaStatus `json:"status"`
		}
		if json.Unmarshal(m.Data, &p) == nil && len(p.Status) > 0 {
			save = s.applyMediaLocked(p.Status[0])
		}

	case m.Namespace == nsMedia && (h.Type == "LOAD_FAILED" || h.Type == "INVALID_REQUEST" || h.Type == "LOAD_CANCELLED" || h.Type == "INVALID_PLAYER_STATE"):
		if s.loadReq == 0 || h.RequestID == s.loadReq {
			s.loadErr = h.Type
			s.st.Error = h.Type
			slog.Warn("chromecast rejected the request", "device", s.dev.Name, "type", h.Type, "payload", string(m.Data))
		}
	}
	s.notifyLocked()
	s.mu.Unlock()

	if save != nil {
		s.m.lib.SetProgress(save.id, save.pos, save.dur)
	}
}

type saveReq struct {
	id       string
	pos, dur float64
}

// applyMediaLocked folds a MEDIA_STATUS into s.st and decides whether progress should be stored.
func (s *session) applyMediaLocked(ms mediaStatus) *saveReq {
	prev := s.st.State
	var cancelled *saveReq
	if ms.PlayerState == "IDLE" && ms.IdleReason == "CANCELLED" && ms.MediaSessionID == s.mediaID {
		cancelled = s.leaveLocked() // before the status below overwrites the position
	}
	s.mediaID = ms.MediaSessionID
	s.stAt = time.Now()
	s.st.Position = ms.CurrentTime
	if ms.Media != nil && ms.Media.Duration > 0 {
		s.st.Duration = ms.Media.Duration
	}
	s.st.IdleReason = ms.IdleReason
	s.st.Sub = ""
	for _, id := range ms.ActiveTrackIDs {
		if sub, ok := s.tracks[id]; ok {
			s.st.Sub = sub
		}
	}
	switch ms.PlayerState {
	case "PLAYING", "PAUSED", "BUFFERING", "IDLE":
		s.st.State = ms.PlayerState
	}
	if s.st.State != "IDLE" {
		s.loadErr, s.st.Error = "", ""
	}
	id := s.st.ItemID
	if id == "" {
		return nil
	}
	switch {
	case s.st.State == "IDLE" && ms.IdleReason == "FINISHED":
		s.mediaID = 0
		s.m.onStop(id)
		return &saveReq{id, s.st.Duration, s.st.Duration}
	case s.st.State == "IDLE":
		return cancelled
	case s.st.State == "PAUSED" && prev != "PAUSED", time.Since(s.lastSave) >= saveEvery:
		s.lastSave = time.Now()
		return &saveReq{id, ms.CurrentTime, s.st.Duration}
	}
	return nil
}

// status returns a snapshot with the position extrapolated while playing.
func (s *session) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Position = s.positionLocked()
	return st
}

// positionLocked is the playback position, extrapolated since the last MEDIA_STATUS while playing.
func (s *session) positionLocked() float64 {
	pos := s.st.Position
	if s.st.State == "PLAYING" && !s.stAt.IsZero() {
		pos += time.Since(s.stAt).Seconds()
		if s.st.Duration > 0 && pos > s.st.Duration {
			pos = s.st.Duration
		}
	}
	return pos
}

// leaveLocked returns the progress to store when playback ends without FINISHED
// (stopped, app closed, cancelled), or nil if nothing meaningful was playing.
func (s *session) leaveLocked() *saveReq {
	if s.st.ItemID == "" || s.mediaID == 0 || s.st.State == "IDLE" || s.st.State == "LOADING" {
		return nil
	}
	return &saveReq{s.st.ItemID, s.positionLocked(), s.st.Duration}
}

// play sends a LOAD for item and waits until the device reports it.
func (s *session) play(ctx context.Context, item model.Item, load loadSpec) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	c, transport, oldMedia := s.c, s.transportID, s.mediaID
	s.tracks = load.trackSubs
	s.st.ItemID, s.st.Audio, s.st.Mode, s.st.Sub = item.ID, load.audio, load.mode, load.activeSub
	s.st.State, s.st.Position, s.st.Error = "LOADING", load.start, ""
	s.st.Duration = load.duration
	s.stAt, s.loadErr, s.loadReq = time.Time{}, "", 0
	s.lastSave = time.Now()
	s.notifyLocked()
	s.mu.Unlock()
	if c == nil {
		return errClosed
	}

	id, err := c.send(nsMedia, transport, load.payload)
	if err != nil {
		s.drop(c)
		return err
	}
	s.mu.Lock()
	s.loadReq = id
	s.mu.Unlock()

	err = s.waitFor(ctx, loadTimeout, func() bool {
		if s.loadErr != "" {
			return true
		}
		return s.mediaID != 0 && s.mediaID != oldMedia && (s.st.State == "PLAYING" || s.st.State == "PAUSED" || s.st.State == "BUFFERING")
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && s.loadErr != "" {
		err = fmt.Errorf("chromecast refused to load the media (%s)", s.loadErr)
	}
	if err != nil {
		s.st.State, s.st.Error = "IDLE", err.Error()
	}
	return err
}

// ControlRequest is a transport command.
type ControlRequest struct {
	Action string          `json:"action"` // pause|resume|seek|stop|volume|mute|sub|audio
	Value  json.RawMessage `json:"value"`
}

func (r ControlRequest) number() (float64, error) {
	var v float64
	if err := json.Unmarshal(r.Value, &v); err != nil {
		return 0, badRequest("%s needs a number value", r.Action)
	}
	return v, nil
}

func (r ControlRequest) text() string {
	var v string
	_ = json.Unmarshal(r.Value, &v)
	return v
}

func (s *session) control(ctx context.Context, req ControlRequest) error {
	s.mu.Lock()
	c, transport, media := s.c, s.transportID, s.mediaID
	session := s.sessionID
	itemID, audio := s.st.ItemID, s.st.Audio
	trackIDs := map[string]int{}
	for id, sub := range s.tracks {
		trackIDs[sub] = id
	}
	s.mu.Unlock()
	if c == nil {
		return errClosed
	}
	needMedia := func() error {
		if transport == "" || media == 0 {
			return badRequest("nothing is playing on this device")
		}
		return nil
	}
	send := func(ns, dst string, p map[string]any) error { _, err := c.send(ns, dst, p); return err }

	switch req.Action {
	case "pause", "resume":
		if err := needMedia(); err != nil {
			return err
		}
		t := map[string]string{"pause": "PAUSE", "resume": "PLAY"}[req.Action]
		return send(nsMedia, transport, map[string]any{"type": t, "mediaSessionId": media})
	case "seek":
		if err := needMedia(); err != nil {
			return err
		}
		v, err := req.number()
		if err != nil {
			return err
		}
		return send(nsMedia, transport, map[string]any{"type": "SEEK", "mediaSessionId": media, "currentTime": v, "resumeState": "PLAYBACK_START"})
	case "stop":
		if itemID != "" {
			if st := s.status(); st.State == "PLAYING" || st.State == "PAUSED" || st.State == "BUFFERING" {
				s.m.lib.SetProgress(itemID, st.Position, st.Duration)
			}
		}
		if session != "" {
			// Closing the receiver app returns the TV to its ambient screen.
			if err := send(nsReceiver, receiverID, map[string]any{"type": "STOP", "sessionId": session}); err != nil {
				return err
			}
		}
		if itemID != "" {
			s.m.onStop(itemID)
		}
		return nil
	case "volume":
		v, err := req.number()
		if err != nil {
			return err
		}
		if v < 0 || v > 1 {
			return badRequest("volume must be between 0 and 1")
		}
		return send(nsReceiver, receiverID, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"level": v}})
	case "mute":
		var b bool
		if err := json.Unmarshal(req.Value, &b); err != nil {
			return badRequest("mute needs true or false")
		}
		return send(nsReceiver, receiverID, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"muted": b}})
	case "sub":
		if err := needMedia(); err != nil {
			return err
		}
		active := []int{}
		if sub := req.text(); sub != "" {
			id, ok := trackIDs[sub]
			if !ok {
				return badRequest("unknown subtitle %q", sub)
			}
			active = append(active, id)
		}
		return send(nsMedia, transport, map[string]any{"type": "EDIT_TRACKS_INFO", "mediaSessionId": media, "activeTrackIds": active})
	case "audio":
		v, err := req.number()
		if err != nil {
			return err
		}
		if itemID == "" {
			return badRequest("nothing is playing on this device")
		}
		if int(v) == audio {
			return nil
		}
		st := s.status()
		return s.m.Play(ctx, s.dev.ID, PlayRequest{ItemID: itemID, Audio: int(v), Sub: st.Sub, Start: st.Position, Mode: "transcode", Resume: "restart"})
	}
	return badRequest("unknown action %q", req.Action)
}

// close ends the connection (used on shutdown).
func (s *session) close() {
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c != nil {
		s.drop(c)
	}
}

func contentTypeForFile(path string) string {
	switch strings.ToLower(path[strings.LastIndex(path, ".")+1:]) {
	case "webm":
		return "video/webm"
	default:
		return "video/mp4"
	}
}
