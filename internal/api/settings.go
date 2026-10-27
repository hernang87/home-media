package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
)

// SettingsView is the body of GET /api/settings. The TMDB key itself is never
// sent, only whether one is set.
type SettingsView struct {
	MediaDir        string   `json:"mediaDir"`
	TMDBLanguage    string   `json:"tmdbLanguage"`
	TMDBKeySet      bool     `json:"tmdbKeySet"`
	Port            int      `json:"port"`
	Overridden      []string `json:"overridden"`      // .env names that mediad's environment overrides
	RestartRequired bool     `json:"restartRequired"` // a saved change waits for a restart
	Editable        bool     `json:"editable"`        // this caller may POST; set by the handler
}

// SettingsUpdate is the body of POST /api/settings. A nil field is left as it
// is; an empty TMDBKey removes the key.
type SettingsUpdate struct {
	MediaDir     *string `json:"mediaDir,omitempty"`
	TMDBLanguage *string `json:"tmdbLanguage,omitempty"`
	TMDBKey      *string `json:"tmdbKey,omitempty"`
}

// InvalidSettingError is a rejected update; the handler answers 400 with its text.
type InvalidSettingError struct{ Msg string }

func (e *InvalidSettingError) Error() string { return e.Msg }

// SettingsStore is what the API needs from the daemon to read and change settings.
type SettingsStore interface {
	Settings() SettingsView
	Update(ctx context.Context, u SettingsUpdate) (SettingsView, error)
}

func (h *handlers) settings(w http.ResponseWriter, r *http.Request) {
	if h.d.Settings == nil {
		notFound(w)
		return
	}
	v := h.d.Settings.Settings()
	v.Editable = isThisMachine(r.RemoteAddr)
	writeJSON(w, http.StatusOK, v)
}

func (h *handlers) updateSettings(w http.ResponseWriter, r *http.Request) {
	if h.d.Settings == nil {
		notFound(w)
		return
	}
	if !isThisMachine(r.RemoteAddr) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "settings can only be changed from the machine running mediad"})
		return
	}
	var u SettingsUpdate
	if err := decodeBody(r, &u); err != nil {
		writeText(w, http.StatusBadRequest, "Bad request")
		return
	}
	v, err := h.d.Settings.Update(r.Context(), u)
	var invalid *InvalidSettingError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalid.Msg})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save settings"})
		return
	}
	v.Editable = true
	writeJSON(w, http.StatusOK, v)
}

// isThisMachine reports whether remote ("host:port") is loopback or one of this
// machine's own addresses. A desktop app that reaches mediad through the LAN
// address of the same Mac arrives from that address, not from loopback; a
// remote host cannot send from an address that belongs to this one.
func isThisMachine(remote string) bool {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	if addr.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if own, ok := netip.AddrFromSlice(ipn.IP); ok && own.Unmap() == addr {
				return true
			}
		}
	}
	return false
}
