// Package api serves the HTTP API and media files. Port of server.js (node-ref).
package api

import (
	"context"
	"net/http"

	"homemedia/internal/config"
	"homemedia/internal/model"
)

// Library is what the API needs from internal/library.
type Library interface {
	Get(id string) (model.Item, bool)
	Snapshot() model.Snapshot
	Status() model.ScanStatus
	Scan(ctx context.Context, refresh bool) error
	SetProgress(id string, pos, dur float64)
	MarkWatched(id string, watched bool)
}

// Transcoder is what the API needs from internal/transcode.
type Transcoder interface {
	Segment(ctx context.Context, item model.Item, audio, n int) (string, error)
}

// Info is returned by GET /api/info.
type Info struct {
	Name    string `json:"name"`
	LAN     string `json:"lan"`
	Port    int    `json:"port"`
	Base    string `json:"base"` // http://<lan>:<port>
	TMDB    bool   `json:"tmdb"`
	Version string `json:"version"`
}

// Deps wires the handlers. Extra is mounted as-is for routes owned by other
// packages (cast); it may be nil.
type Deps struct {
	Config *config.Config
	Lib    Library
	TX     Transcoder
	Info   Info
	Extra  func(mux *http.ServeMux)
}

// New returns the full handler: routes + CORS + LAN-only filter + request logging.
func New(d Deps) http.Handler {
	var h http.Handler = newMux(d)
	h = bodyLimit(h)
	h = cors(h)
	h = lanOnly(h)
	h = requestLog(h)
	return h
}
