package model

import "fmt"

// ServerInfo is the response of GET /api/info.
type ServerInfo struct {
	Name    string `json:"name"`
	LAN     string `json:"lan"`
	Port    int    `json:"port"`
	Base    string `json:"base"` // http://<lan>:<port>
	TMDB    bool   `json:"tmdb"`
	Version string `json:"version"`
}

// Device is a Chromecast listed by GET /api/cast/devices.
type Device struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
}

// CastState is a Chromecast's playback state. PLAYING, PAUSED, BUFFERING and
// IDLE come from the receiver; DISCONNECTED and LOADING are mediad's own.
type CastState string

const (
	StateDisconnected CastState = "DISCONNECTED"
	StateIdle         CastState = "IDLE"
	StateLoading      CastState = "LOADING"
	StatePlaying      CastState = "PLAYING"
	StatePaused       CastState = "PAUSED"
	StateBuffering    CastState = "BUFFERING"
)

// Active reports whether loaded media is playing, paused or buffering.
func (s CastState) Active() bool {
	return s == StatePlaying || s == StatePaused || s == StateBuffering
}

// CastStatus is the response of GET /api/cast/{device}/status.
type CastStatus struct {
	State      CastState `json:"state"`
	Position   float64   `json:"position"`
	Duration   float64   `json:"duration"`
	ItemID     string    `json:"itemId"`
	Audio      int       `json:"audio"`
	Sub        string    `json:"sub"` // sub track id, "" = off
	Mode       string    `json:"mode"`
	Volume     float64   `json:"volume"`
	Muted      bool      `json:"muted"`
	IdleReason string    `json:"idleReason,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Clock formats seconds as m:ss, or h:mm:ss from one hour on.
func Clock(sec float64) string {
	s := int(sec)
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
