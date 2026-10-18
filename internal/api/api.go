// Package api serves the HTTP API and media files.
package api

import (
	"context"
	"net/http"

	"homemedia/internal/config"
	"homemedia/internal/logbuf"
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
type Info = model.ServerInfo

// Deps wires the handlers. Extra is mounted as-is for routes owned by other
// packages (cast); it may be nil.
type Deps struct {
	Config *config.Config
	Lib    Library
	TX     Transcoder
	Info   Info
	Addr   func() (lan, base string) // optional; current LAN address and base URL for /api/info
	Extra  func(mux *http.ServeMux)
	Logs   *logbuf.Buffer // optional; served at GET /api/logs
	// Settings backs /api/settings; nil answers 404. Writes are accepted only
	// from the machine mediad runs on.
	Settings SettingsStore
}

// New returns the full handler: routes + CORS + API origin guard + LAN-only filter + request logging.
func New(d Deps) http.Handler {
	var h http.Handler = newMux(d)
	h = bodyLimit(h)
	h = cors(h)
	h = apiGuard(h)
	h = lanOnly(h)
	h = requestLog(h)
	return h
}
