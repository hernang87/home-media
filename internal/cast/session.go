package cast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
type Status = model.CastStatus

type session struct {
	m      *Manager
	dialMu sync.Mutex // serializes connect

	mu          sync.Mutex
	dev         Device // guarded by mu
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
	return &session{m: m, dev: dev, changed: make(chan struct{}), st: Status{State: model.StateDisconnected}}
}

// device returns the device record; its address can change between connects.
func (s *session) device() Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev
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
	if err := s.connect(); err != nil {
		return err
	}
	return s.ensureApp(ctx)
}

// connect dials the device unless a connection exists. dialMu makes concurrent
// callers share one dial instead of leaking the connection that loses the race.
func (s *session) connect() error {
	s.dialMu.Lock()
	defer s.dialMu.Unlock()
	s.mu.Lock()
	c, dev := s.c, s.dev
	s.mu.Unlock()
	if c != nil {
		return nil
	}
	nc, err := dial(dev.Addr, dev.Port, 5*time.Second)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.c, s.lastRx = nc, time.Now()
	s.st.State = model.StateIdle
	s.mu.Unlock()
	in := make(chan message, 16)
	go nc.readLoop(in)
	go s.dispatch(nc, in)
	go s.heartbeat(nc)
	if _, err := nc.send(nsConnection, receiverID, map[string]any{"type": "CONNECT"}); err != nil {
		s.drop(nc)
		return err
	}
	return nil
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
		s.st.State = model.StateDisconnected
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
		media, transport, playing := s.mediaID, s.transportID, s.st.State == model.StatePlaying || s.st.State == model.StateBuffering
		s.mu.Unlock()
		if !cur {
			return
		}
		if dead {
			slog.Warn("chromecast stopped answering", "device", s.device().Name)
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
	MediaSessionID int             `json:"mediaSessionId"`
	PlayerState    model.CastState `json:"playerState"`
	CurrentTime    float64         `json:"currentTime"`
	IdleReason     string          `json:"idleReason"`
	ActiveTrackIDs []int           `json:"activeTrackIds"`
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
		save = s.applyReceiverLocked(c, m.Data)

	case m.Namespace == nsConnection && h.Type == "CLOSE":
		if m.Source != receiverID {
			save = s.appClosedLocked()
		}

	case m.Namespace == nsMedia && h.Type == "MEDIA_STATUS":
		var p struct {
			Status []mediaStatus `json:"status"`
		}
		if json.Unmarshal(m.Data, &p) == nil && len(p.Status) > 0 {
			save = s.applyMediaLocked(p.Status[0])
		}

	case m.Namespace == nsMedia && loadErrors[h.Type]:
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

// loadErrors are the media replies that mean a LOAD (or other request) failed.
var loadErrors = map[string]bool{"LOAD_FAILED": true, "INVALID_REQUEST": true, "LOAD_CANCELLED": true, "INVALID_PLAYER_STATE": true}

// applyReceiverLocked folds a RECEIVER_STATUS into the session: volume, and
// whether the Default Media Receiver is (still) running.
func (s *session) applyReceiverLocked(c *conn, data []byte) *saveReq {
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
	if json.Unmarshal(data, &p) != nil {
		return nil
	}
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
		return s.appClosedLocked()
	}
	return nil
}

// appClosedLocked forgets the receiver app and returns the progress to store.
func (s *session) appClosedLocked() *saveReq {
	save := s.leaveLocked()
	s.transportID, s.sessionID, s.mediaID = "", "", 0
	s.st.State, s.st.Position, s.st.ItemID = model.StateIdle, 0, ""
	return save
}

type saveReq struct {
	id       string
	pos, dur float64
}

// applyMediaLocked folds a MEDIA_STATUS into s.st and decides whether progress should be stored.
func (s *session) applyMediaLocked(ms mediaStatus) *saveReq {
	prev := s.st.State
	var cancelled *saveReq
	if ms.PlayerState == model.StateIdle && ms.IdleReason == "CANCELLED" && ms.MediaSessionID == s.mediaID {
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
	case model.StatePlaying, model.StatePaused, model.StateBuffering, model.StateIdle:
		s.st.State = ms.PlayerState
	}
	if s.st.State != model.StateIdle {
		s.loadErr, s.st.Error = "", ""
	}
	id := s.st.ItemID
	if id == "" {
		return nil
	}
	switch {
	case s.st.State == model.StateIdle && ms.IdleReason == "FINISHED":
		s.mediaID = 0
		s.m.onStop(id)
		return &saveReq{id, s.st.Duration, s.st.Duration}
	case s.st.State == model.StateIdle:
		return cancelled
	case s.st.State == model.StatePaused && prev != model.StatePaused, time.Since(s.lastSave) >= saveEvery:
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
	if s.st.State == model.StatePlaying && !s.stAt.IsZero() {
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
	if s.st.ItemID == "" || s.mediaID == 0 || s.st.State == model.StateIdle || s.st.State == model.StateLoading {
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
	s.st.State, s.st.Position, s.st.Error = model.StateLoading, load.start, ""
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
		return s.mediaID != 0 && s.mediaID != oldMedia && s.st.State.Active()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && s.loadErr != "" {
		err = fmt.Errorf("chromecast refused to load the media (%s)", s.loadErr)
	}
	if err != nil {
		s.st.State, s.st.Error = model.StateIdle, err.Error()
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

// controlTarget is what a transport command needs from the session, copied
// under s.mu so the commands can send without holding it.
type controlTarget struct {
	c                  *conn
	transport, session string
	media              int
	itemID             string
	audio              int
	trackIDs           map[string]int // sub id -> castTrackId
}

func (s *session) snapshotControl() (controlTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c == nil {
		return controlTarget{}, errClosed
	}
	t := controlTarget{c: s.c, transport: s.transportID, session: s.sessionID, media: s.mediaID, itemID: s.st.ItemID, audio: s.st.Audio, trackIDs: map[string]int{}}
	for id, sub := range s.tracks {
		t.trackIDs[sub] = id
	}
	return t, nil
}

func (t controlTarget) send(ns, dst string, p map[string]any) error {
	_, err := t.c.send(ns, dst, p)
	return err
}

// requireMedia fails when no media session is loaded.
func (t controlTarget) requireMedia() error {
	if t.transport == "" || t.media == 0 {
		return badRequest("nothing is playing on this device")
	}
	return nil
}

// sendMedia sends a media command for the current media session.
func (t controlTarget) sendMedia(p map[string]any) error {
	if err := t.requireMedia(); err != nil {
		return err
	}
	p["mediaSessionId"] = t.media
	return t.send(nsMedia, t.transport, p)
}

func (s *session) control(ctx context.Context, req ControlRequest) error {
	t, err := s.snapshotControl()
	if err != nil {
		return err
	}
	switch req.Action {
	case "pause":
		return t.sendMedia(map[string]any{"type": "PAUSE"})
	case "resume":
		return t.sendMedia(map[string]any{"type": "PLAY"})
	case "seek":
		return t.seek(req)
	case "stop":
		return s.stop(t)
	case "volume":
		return t.volume(req)
	case "mute":
		return t.mute(req)
	case "sub":
		return t.sub(req)
	case "audio":
		return s.switchAudio(ctx, t, req)
	}
	return badRequest("unknown action %q", req.Action)
}

func (t controlTarget) seek(req ControlRequest) error {
	if err := t.requireMedia(); err != nil {
		return err
	}
	v, err := req.number()
	if err != nil {
		return err
	}
	return t.sendMedia(map[string]any{"type": "SEEK", "currentTime": v, "resumeState": "PLAYBACK_START"})
}

func (t controlTarget) volume(req ControlRequest) error {
	v, err := req.number()
	if err != nil {
		return err
	}
	if v < 0 || v > 1 {
		return badRequest("volume must be between 0 and 1")
	}
	return t.send(nsReceiver, receiverID, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"level": v}})
}

func (t controlTarget) mute(req ControlRequest) error {
	var b bool
	if err := json.Unmarshal(req.Value, &b); err != nil {
		return badRequest("mute needs true or false")
	}
	return t.send(nsReceiver, receiverID, map[string]any{"type": "SET_VOLUME", "volume": map[string]any{"muted": b}})
}

func (t controlTarget) sub(req ControlRequest) error {
	if err := t.requireMedia(); err != nil {
		return err
	}
	active := []int{}
	if sub := req.text(); sub != "" {
		id, ok := t.trackIDs[sub]
		if !ok {
			return badRequest("unknown subtitle %q", sub)
		}
		active = append(active, id)
	}
	return t.sendMedia(map[string]any{"type": "EDIT_TRACKS_INFO", "activeTrackIds": active})
}

// stop saves progress and closes the receiver app, which returns the TV to its
// ambient screen.
func (s *session) stop(t controlTarget) error {
	if t.itemID != "" {
		if st := s.status(); st.State.Active() {
			s.m.lib.SetProgress(t.itemID, st.Position, st.Duration)
		}
	}
	if t.session != "" {
		if err := t.send(nsReceiver, receiverID, map[string]any{"type": "STOP", "sessionId": t.session}); err != nil {
			return err
		}
	}
	if t.itemID != "" {
		s.m.onStop(t.itemID)
	}
	return nil
}

// switchAudio reloads the item with another audio track from the current position.
func (s *session) switchAudio(ctx context.Context, t controlTarget, req ControlRequest) error {
	v, err := req.number()
	if err != nil {
		return err
	}
	if t.itemID == "" {
		return badRequest("nothing is playing on this device")
	}
	if int(v) == t.audio {
		return nil
	}
	st := s.status()
	return s.m.Play(ctx, s.device().ID, PlayRequest{ItemID: t.itemID, Audio: int(v), Sub: st.Sub, Start: st.Position, Mode: "transcode", Resume: "restart"})
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
