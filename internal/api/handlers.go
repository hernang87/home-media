package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"homemedia/internal/logbuf"
	"homemedia/internal/model"
	"homemedia/internal/subs"
	"homemedia/internal/transcode"
)

// fileTypes maps file extensions to the Content-Type served for them.
var fileTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".json": "application/json",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".ico": "image/x-icon",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/mp4", ".mkv": "video/x-matroska", ".webm": "video/webm", ".avi": "video/x-msvideo", ".ts": "video/mp2t",
}

var (
	reID    = regexp.MustCompile(`^\w+$`)
	reArtID = regexp.MustCompile(`^[\w-]+$`)
	reDigit = regexp.MustCompile(`^[0-9]+$`)
	reSeg   = regexp.MustCompile(`^seg([0-9]+)\.ts$`)
)

// handlers holds the dependencies of the route handlers.
type handlers struct {
	d Deps
}

// newMux registers the built-in routes, then the Extra routes.
func newMux(d Deps) *http.ServeMux {
	h := &handlers{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", h.info)
	mux.HandleFunc("GET /api/library", h.library)
	mux.HandleFunc("GET /api/status", h.status)
	mux.HandleFunc("POST /api/scan", h.scan)
	mux.HandleFunc("POST /api/progress", h.progress)
	mux.HandleFunc("GET /api/logs", h.logs)
	mux.HandleFunc("GET /api/settings", h.settings)
	mux.HandleFunc("POST /api/settings", h.updateSettings)
	mux.HandleFunc("GET /art/{id}/{kind}", h.art)
	mux.HandleFunc("GET /file/{id}", h.file)
	mux.HandleFunc("GET /hls/{id}/{audio}/index.m3u8", h.playlist)
	mux.HandleFunc("GET /hls/{id}/{audio}/{seg}", h.segment)
	mux.HandleFunc("GET /sub/{id}/{subfile}", h.subtitle)
	if d.Extra != nil {
		d.Extra(mux)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeText writes msg; it keeps a Content-Type the handler already set.
func writeText(w http.ResponseWriter, status int, msg string) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

// parseDigits accepts only ASCII digits.
func parseDigits(s string) (int, bool) {
	if !reDigit.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// openRegular opens path and rejects anything that is not a regular file.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, nil, err
	}
	return f, fi, nil
}

// serveFile writes a file with Range support (via http.ServeContent) and the given headers.
func serveFile(w http.ResponseWriter, r *http.Request, f *os.File, fi os.FileInfo, contentType, cacheControl string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// decodeBody reads a JSON body. An empty body decodes to the zero value.
func decodeBody(r *http.Request, v any) error {
	err := json.NewDecoder(r.Body).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (h *handlers) info(w http.ResponseWriter, r *http.Request) {
	info := h.d.Info
	if h.d.Addr != nil {
		info.LAN, info.Base = h.d.Addr()
	}
	if h.d.Settings != nil {
		info.TMDB = h.d.Settings.Settings().TMDBKeySet // the key can change while we run
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *handlers) library(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.d.Lib.Snapshot())
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.d.Lib.Status())
}

func (h *handlers) scan(w http.ResponseWriter, r *http.Request) {
	_, refresh := r.URL.Query()["refresh"]
	go func() {
		if err := h.d.Lib.Scan(context.Background(), refresh); err != nil {
			slog.Error("library scan failed", "err", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type progressRequest struct {
	ID       string  `json:"id"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Watched  *bool   `json:"watched"`
}

func (h *handlers) progress(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if err := decodeBody(r, &req); err != nil {
		writeText(w, http.StatusBadRequest, "Bad request")
		return
	}
	if _, ok := h.d.Lib.Get(req.ID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown item"})
		return
	}
	if req.Watched != nil {
		h.d.Lib.MarkWatched(req.ID, *req.Watched)
	} else {
		h.d.Lib.SetProgress(req.ID, req.Position, req.Duration)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handlers) art(w http.ResponseWriter, r *http.Request) {
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if h.d.Config == nil || !reArtID.MatchString(id) || (kind != "poster" && kind != "backdrop") {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	// Items without a poster fall back to their video frame.
	f, fi, err := openRegular(filepath.Join(h.d.Config.ArtDir, id+"."+kind+".jpg"))
	if err != nil {
		f, fi, err = openRegular(filepath.Join(h.d.Config.ArtDir, id+".backdrop.jpg"))
	}
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	defer f.Close()
	serveFile(w, r, f, fi, "image/jpeg", "public, max-age=86400")
}

func (h *handlers) file(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, ok := h.lookup(id)
	if !ok {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	f, fi, err := openRegular(item.Path)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	defer f.Close()
	ctype := fileTypes[strings.ToLower(filepath.Ext(item.Path))]
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	serveFile(w, r, f, fi, ctype, "no-cache")
}

// lookup returns the item for a well-formed id that is in the library.
func (h *handlers) lookup(id string) (model.Item, bool) {
	if !reID.MatchString(id) {
		return model.Item{}, false
	}
	return h.d.Lib.Get(id)
}

// validAudio reports whether audio names a track of item. An item without
// audio tracks has one implicit track, 0, when ffmpeg maps "0:a:0?".
func validAudio(item model.Item, audio int) bool {
	if len(item.Info.Audio) == 0 {
		return audio == 0
	}
	return audio < len(item.Info.Audio)
}

func (h *handlers) playlist(w http.ResponseWriter, r *http.Request) {
	audio, ok := parseDigits(r.PathValue("audio"))
	if !ok {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	item, ok := h.lookup(r.PathValue("id"))
	if !ok || item.Info == nil || !validAudio(item, audio) {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	writeText(w, http.StatusOK, transcode.Playlist(item.Info.Duration))
}

func (h *handlers) segment(w http.ResponseWriter, r *http.Request) {
	audio, ok := parseDigits(r.PathValue("audio"))
	if !ok {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	m := reSeg.FindStringSubmatch(r.PathValue("seg"))
	if m == nil {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	n, ok := parseDigits(m[1])
	if !ok {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	item, ok := h.lookup(r.PathValue("id"))
	if !ok || item.Info == nil || !validAudio(item, audio) || n >= transcode.SegmentCount(item.Info.Duration) {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	path, err := h.d.TX.Segment(r.Context(), item, audio, n)
	if r.Context().Err() != nil {
		return // client went away; nothing to write
	}
	if err != nil {
		slog.Warn("segment failed", "item", item.Rel, "audio", audio, "seg", n, "err", err)
		writeText(w, http.StatusServiceUnavailable, "Transcode failed")
		return
	}
	f, fi, err := openRegular(path)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	defer f.Close()
	serveFile(w, r, f, fi, "video/mp2t", "public, max-age=3600")
}

func (h *handlers) subtitle(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("subfile"), ".vtt")
	if !ok || !reID.MatchString(name) {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	item, ok := h.lookup(r.PathValue("id"))
	if !ok {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	var track *model.SubTrack
	for i := range item.Subs {
		if item.Subs[i].ID == name {
			track = &item.Subs[i]
			break
		}
	}
	if track == nil {
		writeText(w, http.StatusNotFound, "Not found")
		return
	}
	vtt, err := subs.LoadVTT(item.Path, *track)
	if err != nil {
		slog.Error("subtitle conversion failed", "item", item.Rel, "sub", track.ID, "err", err)
		writeText(w, http.StatusInternalServerError, "Subtitle conversion failed")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeText(w, http.StatusOK, vtt)
}

// logs returns the recent server log entries newer than ?after=<seq>.
func (h *handlers) logs(w http.ResponseWriter, r *http.Request) {
	entries, last := []logbuf.Entry{}, 0
	if h.d.Logs != nil {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		entries = append(entries, h.d.Logs.After(after)...)
		last = h.d.Logs.Last()
	}
	// last lets a client notice a server restart (the sequence starts over).
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "last": last})
}
