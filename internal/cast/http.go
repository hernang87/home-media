package cast

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Routes mounts the cast API on mux (see Deps.Extra in internal/api).
func (m *Manager) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cast/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.Devices())
	})
	mux.HandleFunc("GET /api/cast/{device}/status", func(w http.ResponseWriter, r *http.Request) {
		st, err := m.Status(r.PathValue("device"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/cast/{device}/play", func(w http.ResponseWriter, r *http.Request) {
		var req PlayRequest
		if !readJSON(w, r, &req) {
			return
		}
		// The caller disconnecting must not abort a half-finished LOAD, but bound it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), loadTimeout+20*time.Second)
		defer cancel()
		if err := m.Play(ctx, r.PathValue("device"), req); err != nil {
			writeErr(w, err)
			return
		}
		st, _ := m.Status(r.PathValue("device"))
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/cast/{device}/control", func(w http.ResponseWriter, r *http.Request) {
		var req ControlRequest
		if !readJSON(w, r, &req) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := m.Control(ctx, r.PathValue("device"), req); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway // the device or its network misbehaved
	var re *ResumeError
	if errors.As(err, &re) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": re.Error(), "code": "resume", "position": re.Position, "duration": re.Duration})
		return
	}
	switch {
	case errors.Is(err, ErrNoDevice):
		code = http.StatusNotFound
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, errBadRequest):
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
