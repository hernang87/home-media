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

// CastStatus is the response of GET /api/cast/{device}/status.
type CastStatus struct {
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
