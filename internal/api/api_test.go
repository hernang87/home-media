package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"homemedia/internal/config"
	"homemedia/internal/logbuf"
	"homemedia/internal/model"
	"homemedia/internal/transcode"
)

const lanRemote = "192.168.1.10:5000"

type fakeLib struct {
	items map[string]model.Item
	calls []string
	scans chan bool
}

func (f *fakeLib) Get(id string) (model.Item, bool) {
	it, ok := f.items[id]
	return it, ok
}
func (f *fakeLib) Snapshot() model.Snapshot {
	return model.Snapshot{Items: []model.PublicItem{{ID: "a", Title: "A"}}}
}
func (f *fakeLib) Status() model.ScanStatus { return model.ScanStatus{Phase: "idle"} }
func (f *fakeLib) Scan(ctx context.Context, refresh bool) error {
	f.scans <- refresh
	return nil
}
func (f *fakeLib) SetProgress(id string, pos, dur float64) {
	f.calls = append(f.calls, "progress "+id+" "+ftoa(pos)+" "+ftoa(dur))
}
func (f *fakeLib) MarkWatched(id string, watched bool) {
	if watched {
		f.calls = append(f.calls, "watched "+id+" true")
	} else {
		f.calls = append(f.calls, "watched "+id+" false")
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

type fakeTX struct {
	dir  string
	err  error
	seen []int
}

func (f *fakeTX) Segment(ctx context.Context, item model.Item, audio, n int) (string, error) {
	f.seen = append(f.seen, n)
	if f.err != nil {
		return "", f.err
	}
	p := filepath.Join(f.dir, transcode.SegmentName(n))
	if err := os.WriteFile(p, []byte("TSDATA"), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

type fixture struct {
	h   http.Handler
	lib *fakeLib
	tx  *fakeTX
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mp4")
	must(t, os.WriteFile(video, []byte("0123456789"), 0o644))
	srt := filepath.Join(dir, "movie.en.srt")
	must(t, os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o644))

	artDir := filepath.Join(dir, "art")
	must(t, os.MkdirAll(artDir, 0o755))
	must(t, os.WriteFile(filepath.Join(artDir, "a.poster.jpg"), []byte("POSTER"), 0o644))
	must(t, os.WriteFile(filepath.Join(artDir, "a.backdrop.jpg"), []byte("BACKDROP"), 0o644))
	must(t, os.WriteFile(filepath.Join(artDir, "b.backdrop.jpg"), []byte("BACKDROP-B"), 0o644))

	lib := &fakeLib{
		scans: make(chan bool, 4),
		items: map[string]model.Item{
			"a": {
				ID: "a", Path: video, Rel: "movie.mp4",
				Info: &model.MediaInfo{Duration: 13, Audio: []model.AudioTrack{{}, {}}},
				Subs: []model.SubTrack{
					{ID: "e0", Source: "external", File: srt, Lang: "en"},
					{ID: "e1", Source: "external", File: filepath.Join(dir, "missing.srt")},
				},
			},
			"b": {ID: "b", Path: video, Rel: "b.mp4"},                                       // no media info
			"c": {ID: "c", Path: video, Rel: "c.mp4", Info: &model.MediaInfo{Duration: 13}}, // no audio tracks
		},
	}
	tx := &fakeTX{dir: t.TempDir()}
	h := New(Deps{
		Config: &config.Config{ArtDir: artDir},
		Lib:    lib,
		TX:     tx,
		Info:   Info{Name: "Home", LAN: "192.168.1.10", Port: 8200, Base: "http://192.168.1.10:8200", Version: "test"},
	})
	return &fixture{h: h, lib: lib, tx: tx}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func request(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = lanRemote
	req.Host = "192.168.1.10:8200"
	return req
}

func TestRoutes(t *testing.T) {
	fx := newFixture(t)
	rangeHdr := map[string]string{"Range": "bytes=2-5"}
	badRange := map[string]string{"Range": "bytes=20-"}

	tests := []struct {
		name     string
		method   string
		target   string
		headers  map[string]string
		body     string
		status   int
		wantHdr  map[string]string
		wantBody string
	}{
		{name: "info", method: "GET", target: "/api/info", status: 200,
			wantHdr:  map[string]string{"Cache-Control": "no-store", "Content-Type": "application/json"},
			wantBody: `"name":"Home"`},
		{name: "library", method: "GET", target: "/api/library", status: 200,
			wantHdr: map[string]string{"Cache-Control": "no-store"}, wantBody: `"title":"A"`},
		{name: "status", method: "GET", target: "/api/status", status: 200,
			wantHdr: map[string]string{"Cache-Control": "no-store"}, wantBody: `"phase":"idle"`},

		{name: "art poster", method: "GET", target: "/art/a/poster", status: 200,
			wantHdr:  map[string]string{"Content-Type": "image/jpeg", "Cache-Control": "public, max-age=86400"},
			wantBody: "POSTER"},
		{name: "art backdrop", method: "GET", target: "/art/a/backdrop", status: 200, wantBody: "BACKDROP"},
		{name: "art poster falls back to backdrop", method: "GET", target: "/art/b/poster", status: 200, wantBody: "BACKDROP-B"},
		{name: "art unknown kind", method: "GET", target: "/art/a/thumb", status: 404},
		{name: "art missing", method: "GET", target: "/art/zz/poster", status: 404},
		{name: "art bad id", method: "GET", target: "/art/..x/poster", status: 404},

		{name: "file full", method: "GET", target: "/file/a", status: 200,
			wantHdr:  map[string]string{"Content-Type": "video/mp4", "Cache-Control": "no-cache", "Accept-Ranges": "bytes"},
			wantBody: "0123456789"},
		{name: "file range", method: "GET", target: "/file/a", headers: rangeHdr, status: 206,
			wantHdr:  map[string]string{"Content-Range": "bytes 2-5/10", "Content-Length": "4"},
			wantBody: "2345"},
		{name: "file range unsatisfiable", method: "GET", target: "/file/a", headers: badRange, status: 416,
			wantHdr: map[string]string{"Content-Range": "bytes */10"}},
		{name: "file head", method: "HEAD", target: "/file/a", status: 200,
			wantHdr: map[string]string{"Content-Length": "10", "Content-Type": "video/mp4"}},
		{name: "file unknown id", method: "GET", target: "/file/nope", status: 404},

		{name: "hls playlist", method: "GET", target: "/hls/a/0/index.m3u8", status: 200,
			wantHdr:  map[string]string{"Content-Type": "application/vnd.apple.mpegurl", "Cache-Control": "no-cache"},
			wantBody: "seg00002.ts"},
		{name: "hls playlist no info", method: "GET", target: "/hls/b/0/index.m3u8", status: 404},
		{name: "hls playlist bad audio", method: "GET", target: "/hls/a/x/index.m3u8", status: 404},
		{name: "hls segment", method: "GET", target: "/hls/a/0/seg00000.ts", status: 200,
			wantHdr:  map[string]string{"Content-Type": "video/mp2t", "Cache-Control": "public, max-age=3600"},
			wantBody: "TSDATA"},
		{name: "hls segment parse neg", method: "GET", target: "/hls/a/0/seg-1.ts", status: 404},
		{name: "hls segment parse letters", method: "GET", target: "/hls/a/0/segX.ts", status: 404},
		{name: "hls segment parse wrong ext", method: "GET", target: "/hls/a/0/seg1.m3u8", status: 404},
		{name: "hls segment parse no ext", method: "GET", target: "/hls/a/0/seg1", status: 404},
		{name: "hls segment bad audio", method: "GET", target: "/hls/a/+1/seg00000.ts", status: 404},
		{name: "hls segment no info", method: "GET", target: "/hls/b/0/seg00000.ts", status: 404},

		{name: "sub vtt", method: "GET", target: "/sub/a/e0.vtt", status: 200,
			wantHdr:  map[string]string{"Content-Type": "text/vtt; charset=utf-8", "Cache-Control": "public, max-age=3600"},
			wantBody: "WEBVTT"},
		{name: "sub unknown track", method: "GET", target: "/sub/a/zz.vtt", status: 404},
		{name: "sub no vtt suffix", method: "GET", target: "/sub/a/e0.srt", status: 404},
		{name: "sub unknown item", method: "GET", target: "/sub/nope/e0.vtt", status: 404},
		{name: "sub conversion error", method: "GET", target: "/sub/a/e1.vtt", status: 500, wantBody: "Subtitle conversion failed"},

		{name: "progress position", method: "POST", target: "/api/progress", status: 200,
			body: `{"id":"a","position":12.5,"duration":100}`, wantBody: `"ok":true`},
		{name: "progress watched", method: "POST", target: "/api/progress", status: 200,
			body: `{"id":"a","watched":true}`, wantBody: `"ok":true`},
		{name: "progress unknown item", method: "POST", target: "/api/progress", status: 404,
			body: `{"id":"nope","position":1}`, wantBody: `{"error":"unknown item"}`},
		{name: "progress empty body", method: "POST", target: "/api/progress", status: 404,
			wantBody: `{"error":"unknown item"}`},
		{name: "progress bad json", method: "POST", target: "/api/progress", status: 400,
			body: `{"id":`},
		{name: "progress body too large", method: "POST", target: "/api/progress", status: 400,
			body: `{"id":"a","pad":"` + strings.Repeat("x", 70<<10) + `"}`},

		{name: "scan", method: "POST", target: "/api/scan?refresh=1", status: 200, wantBody: `{"ok":true}`},
		{name: "scan get not allowed", method: "GET", target: "/api/scan", status: 405},
		{name: "unknown path", method: "GET", target: "/nope", status: 404},

		{name: "cors preflight", method: "OPTIONS", target: "/file/a", status: 204,
			wantHdr: map[string]string{
				"Access-Control-Allow-Origin":   "*",
				"Access-Control-Allow-Methods":  "GET, HEAD, POST, OPTIONS",
				"Access-Control-Allow-Headers":  "Range, Content-Type",
				"Access-Control-Expose-Headers": "Content-Length, Content-Range, Accept-Ranges",
			}},
		{name: "cors on media response", method: "GET", target: "/file/a", status: 200,
			wantHdr: map[string]string{"Access-Control-Allow-Origin": "*"}},
		{name: "api has no cors", method: "GET", target: "/api/info", status: 200,
			wantHdr: map[string]string{"Access-Control-Allow-Origin": ""}},
		{name: "api preflight has no cors", method: "OPTIONS", target: "/api/info", status: 204,
			wantHdr: map[string]string{"Access-Control-Allow-Origin": "", "Access-Control-Allow-Methods": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "scan" {
				defer func() { <-fx.lib.scans }()
			}
			req := request(tt.method, tt.target, tt.body)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			fx.h.ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tt.status, rec.Body.String())
			}
			for k, want := range tt.wantHdr {
				if got := rec.Header().Get(k); got != want {
					t.Errorf("header %s = %q, want %q", k, got, want)
				}
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if tt.method == "HEAD" && rec.Body.Len() != 0 {
				t.Errorf("HEAD body length = %d, want 0", rec.Body.Len())
			}
		})
	}
}

func TestProgressCalls(t *testing.T) {
	fx := newFixture(t)
	for _, body := range []string{
		`{"id":"a","position":12.5,"duration":100}`,
		`{"id":"a","watched":true}`,
		`{"id":"a","watched":false}`,
	} {
		rec := httptest.NewRecorder()
		fx.h.ServeHTTP(rec, request("POST", "/api/progress", body))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
	}
	want := []string{"progress a 12.5 100", "watched a true", "watched a false"}
	if strings.Join(fx.lib.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", fx.lib.calls, want)
	}
}

func TestScanRefreshFlag(t *testing.T) {
	fx := newFixture(t)
	for target, want := range map[string]bool{"/api/scan?refresh=1": true, "/api/scan": false} {
		rec := httptest.NewRecorder()
		fx.h.ServeHTTP(rec, request("POST", target, ""))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		select {
		case got := <-fx.lib.scans:
			if got != want {
				t.Errorf("%s: refresh = %v, want %v", target, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: Scan was not called", target)
		}
	}
}

func TestSegmentCancelledWritesNothing(t *testing.T) {
	fx := newFixture(t)
	fx.tx.err = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", "/hls/a/0/seg00000.ts", nil)
	req.RemoteAddr = lanRemote
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 || rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("cancelled request wrote code %d body %q", rec.Code, rec.Body.String())
	}
}

func TestSegmentErrorIs503(t *testing.T) {
	fx := newFixture(t)
	fx.tx.err = errors.New("boom")
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, request("GET", "/hls/a/0/seg00001.ts", ""))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "Transcode failed" {
		t.Fatalf("got %d %q, want 503 Transcode failed", rec.Code, rec.Body.String())
	}
}

func TestSegmentNumberPassedThrough(t *testing.T) {
	fx := newFixture(t)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, request("GET", "/hls/a/1/seg00002.ts", ""))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fx.tx.seen) != 1 || fx.tx.seen[0] != 2 {
		t.Fatalf("Segment called with %v, want [2]", fx.tx.seen)
	}
}

func TestSegmentAndPlaylistRange(t *testing.T) {
	// Item a has 2 audio tracks and 3 segments (duration 13); item c has no audio tracks.
	tests := []struct {
		name       string
		target     string
		status     int
		wantCalled bool
	}{
		{"last audio track", "/hls/a/1/seg00002.ts", 200, true},
		{"audio past end", "/hls/a/2/seg00000.ts", 404, false},
		{"last segment", "/hls/a/0/seg00002.ts", 200, true},
		{"segment past end", "/hls/a/0/seg00003.ts", 404, false},
		{"no audio tracks, audio 0", "/hls/c/0/seg00000.ts", 200, true},
		{"no audio tracks, audio 1", "/hls/c/1/seg00000.ts", 404, false},
		{"playlist audio past end", "/hls/a/2/index.m3u8", 404, false},
		{"playlist no audio tracks, audio 1", "/hls/c/1/index.m3u8", 404, false},
		{"playlist valid audio", "/hls/a/1/index.m3u8", 200, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFixture(t)
			rec := httptest.NewRecorder()
			fx.h.ServeHTTP(rec, request("GET", tt.target, ""))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tt.status, rec.Body.String())
			}
			if rec.Code == 404 && rec.Body.String() != "Not found" {
				t.Errorf("body = %q, want Not found", rec.Body.String())
			}
			if called := len(fx.tx.seen) > 0; called != tt.wantCalled {
				t.Errorf("transcoder called = %v, want %v", called, tt.wantCalled)
			}
		})
	}
}

func TestRequestLevel(t *testing.T) {
	tests := []struct {
		name   string
		target string
		rng    string
		want   slog.Level
	}{
		{"file no range", "/file/a", "", slog.LevelInfo},
		{"file range from zero", "/file/a", "bytes=0-", slog.LevelInfo},
		{"file range from zero with end", "/file/a", "bytes=0-99", slog.LevelInfo},
		{"file seek range", "/file/a", "bytes=5000-", slog.LevelDebug},
		{"file suffix range", "/file/a", "bytes=-500", slog.LevelDebug},
		{"sub", "/sub/a/e0.vtt", "", slog.LevelInfo},
		{"playlist", "/hls/a/0/index.m3u8", "", slog.LevelInfo},
		{"segment", "/hls/a/0/seg00000.ts", "", slog.LevelDebug},
		{"api", "/api/info", "", slog.LevelDebug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.target, nil)
			if tt.rng != "" {
				req.Header.Set("Range", tt.rng)
			}
			if got := requestLevel(req); got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLANFilter(t *testing.T) {
	fx := newFixture(t)
	tests := []struct {
		remote string
		want   int
	}{
		{"8.8.8.8:1234", 403},
		{"[2001:4860:4860::8888]:1234", 403},
		{"not-an-ip", 403},
		{"127.0.0.1:1", 200},
		{"[::1]:1", 200},
		{"10.1.2.3:1", 200},
		{"172.16.0.1:1", 200},
		{"192.168.1.10:5000", 200},
		{"169.254.1.1:1", 200},
		{"[fe80::1%en0]:1", 200},
		{"[fd00::1]:1", 200},
		{"[::ffff:192.168.1.5]:1", 200},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/info", nil)
			req.RemoteAddr = tt.remote
			req.Host = "192.168.1.10:8200"
			rec := httptest.NewRecorder()
			fx.h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestExtraRoutesMounted(t *testing.T) {
	fx := newFixture(t)
	h := New(Deps{
		Config: &config.Config{ArtDir: t.TempDir()},
		Lib:    fx.lib,
		TX:     fx.tx,
		Extra: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /cast/ping", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			})
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("GET", "/cast/ping", ""))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("extra route status = %d, want 418", rec.Code)
	}
}

func TestLogsEndpoint(t *testing.T) {
	buf := logbuf.New(10)
	slog.New(buf.Handler(slog.NewTextHandler(io.Discard, nil))).Info("hello", "k", "v")
	h := New(Deps{Config: &config.Config{}, Logs: buf})
	for _, tc := range []struct {
		url  string
		want int
	}{{"/api/logs", 1}, {"/api/logs?after=1", 0}} {
		req := httptest.NewRequest("GET", tc.url, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Host = "127.0.0.1:8200"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct {
			Entries []logbuf.Entry `json:"entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Entries) != tc.want {
			t.Fatalf("%s: %v %s", tc.url, err, rec.Body.String())
		}
		if tc.want == 1 && out.Entries[0].Msg != "hello k=v" {
			t.Errorf("entry = %+v", out.Entries[0])
		}
	}
}

func TestAPIGuard(t *testing.T) {
	fx := newFixture(t)
	tests := []struct {
		name   string
		method string
		target string
		host   string
		origin string
		want   int
	}{
		{"no origin", "GET", "/api/info", "192.168.1.10:8200", "", 200},
		{"same origin", "POST", "/api/scan", "192.168.1.10:8200", "http://192.168.1.10:8200", 200},
		{"foreign origin post", "POST", "/api/scan", "192.168.1.10:8200", "https://evil.example", 403},
		{"foreign origin get", "GET", "/api/info", "192.168.1.10:8200", "https://evil.example", 403},
		{"foreign preflight", "OPTIONS", "/api/scan", "192.168.1.10:8200", "https://evil.example", 403},
		{"null origin", "POST", "/api/scan", "192.168.1.10:8200", "null", 403},
		{"rebinding host", "GET", "/api/info", "evil.example:8200", "http://evil.example:8200", 403},
		{"mdns host", "GET", "/api/info", "mac.local:8200", "", 200},
		{"short host", "GET", "/api/info", "macbook:8200", "", 200},
		{"localhost", "GET", "/api/info", "localhost:8200", "", 200},
		{"ipv6 host", "GET", "/api/info", "[fd00::1]:8200", "", 200},
		{"media keeps cors", "GET", "/file/a", "192.168.1.10:8200", "https://www.gstatic.com", 200},
		{"media preflight", "OPTIONS", "/file/a", "192.168.1.10:8200", "https://www.gstatic.com", 204},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "same origin" {
				defer func() { <-fx.lib.scans }()
			}
			req := request(tt.method, tt.target, "")
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			fx.h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestInfoFollowsAddr(t *testing.T) {
	lan, base := "192.168.1.10", "http://192.168.1.10:8200"
	h := New(Deps{Config: &config.Config{}, Info: Info{Name: "n", LAN: lan, Base: base}, Addr: func() (string, string) { return lan, base }})
	get := func() Info {
		req := httptest.NewRequest("GET", "/api/info", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Host = "127.0.0.1:8200"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out Info
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := get(); got.Base != base || got.Name != "n" {
		t.Fatalf("info = %+v", got)
	}
	lan, base = "10.0.0.5", "http://10.0.0.5:8200"
	if got := get(); got.LAN != "10.0.0.5" || got.Base != base {
		t.Errorf("after the address changed, info = %+v", got)
	}
}
