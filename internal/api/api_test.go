package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"homemedia/internal/config"
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
				Info: &model.MediaInfo{Duration: 13},
				Subs: []model.SubTrack{
					{ID: "e0", Source: "external", File: srt, Lang: "en"},
					{ID: "e1", Source: "external", File: filepath.Join(dir, "missing.srt")},
				},
			},
			"b": {ID: "b", Path: video, Rel: "b.mp4"}, // no media info
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

		{name: "cors preflight", method: "OPTIONS", target: "/api/info", status: 204,
			wantHdr: map[string]string{
				"Access-Control-Allow-Origin":   "*",
				"Access-Control-Allow-Methods":  "GET, HEAD, POST, OPTIONS",
				"Access-Control-Allow-Headers":  "Range, Content-Type",
				"Access-Control-Expose-Headers": "Content-Length, Content-Range, Accept-Ranges",
			}},
		{name: "cors on response", method: "GET", target: "/api/info", status: 200,
			wantHdr: map[string]string{"Access-Control-Allow-Origin": "*"}},
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
	fx.h.ServeHTTP(rec, request("GET", "/hls/a/2/seg00003.ts", ""))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fx.tx.seen) != 1 || fx.tx.seen[0] != 3 {
		t.Fatalf("Segment called with %v, want [3]", fx.tx.seen)
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
