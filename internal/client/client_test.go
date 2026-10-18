package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// recorded is what the fake server saw for one request.
type recorded struct {
	method      string
	escapedPath string
	query       string
	contentType string
	body        string
}

// recordingServer answers every request with status and resp and returns a
// getter for the last request seen. The getter is safe to call from the test
// goroutine while the handler runs on the server's goroutines.
func recordingServer(t *testing.T, status int, resp string) (*httptest.Server, func() recorded) {
	t.Helper()
	var mu sync.Mutex
	var last recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = recorded{
			method:      r.Method,
			escapedPath: r.URL.EscapedPath(),
			query:       r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"),
			body:        string(b),
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	get := func() recorded {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
	return srv, get
}

// fakeServer is recordingServer wrapped in a client. The base URL carries a
// trailing slash on purpose, to exercise New's trimming.
func fakeServer(t *testing.T, status int, resp string) (*Client, func() recorded) {
	t.Helper()
	srv, get := recordingServer(t, status, resp)
	return New(srv.URL + "/"), get
}

func strp(s string) *string { return &s }

func TestMethodsHappyPath(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		resp       string
		do         func(c *Client) error
		wantMethod string
		wantPath   string // escaped path
		wantQuery  string
		wantCT     string
		wantBody   map[string]any // nil with wantNoBody = no body expected
		wantNoBody bool
	}{
		{
			name:       "info",
			resp:       `{"name":"box","port":8080,"version":"1.2"}`,
			do:         func(c *Client) error { _, err := c.Info(ctx); return err },
			wantMethod: http.MethodGet, wantPath: "/api/info", wantNoBody: true,
		},
		{
			name:       "library",
			resp:       `{"items":[],"shows":[]}`,
			do:         func(c *Client) error { _, err := c.Library(ctx); return err },
			wantMethod: http.MethodGet, wantPath: "/api/library", wantNoBody: true,
		},
		{
			name:       "scan without refresh",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.Scan(ctx, false) },
			wantMethod: http.MethodPost, wantPath: "/api/scan", wantNoBody: true,
		},
		{
			name:       "scan with refresh",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.Scan(ctx, true) },
			wantMethod: http.MethodPost, wantPath: "/api/scan", wantQuery: "refresh=1", wantNoBody: true,
		},
		{
			name:       "set watched",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.SetWatched(ctx, "ab12", true) },
			wantMethod: http.MethodPost, wantPath: "/api/progress", wantCT: "application/json",
			wantBody: map[string]any{"id": "ab12", "watched": true},
		},
		{
			name:       "set unwatched",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.SetWatched(ctx, "ab12", false) },
			wantMethod: http.MethodPost, wantPath: "/api/progress", wantCT: "application/json",
			wantBody: map[string]any{"id": "ab12", "watched": false},
		},
		{
			name:       "settings",
			resp:       `{"mediaDir":"/m","tmdbLanguage":"en-US","tmdbKeySet":true,"port":8200,"overridden":[],"restartRequired":false,"editable":true}`,
			do:         func(c *Client) error { _, err := c.Settings(ctx); return err },
			wantMethod: http.MethodGet, wantPath: "/api/settings", wantNoBody: true,
		},
		{
			name: "save settings sends only the set fields",
			resp: `{"mediaDir":"/m","tmdbLanguage":"es-AR","tmdbKeySet":true,"editable":true}`,
			do: func(c *Client) error {
				lang := "es-AR"
				_, err := c.SaveSettings(ctx, SettingsUpdate{TMDBLanguage: &lang})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/api/settings", wantCT: "application/json",
			wantBody: map[string]any{"tmdbLanguage": "es-AR"},
		},
		{
			name: "save settings can send an empty key to remove it",
			resp: `{"tmdbKeySet":false}`,
			do: func(c *Client) error {
				empty := ""
				_, err := c.SaveSettings(ctx, SettingsUpdate{TMDBKey: &empty})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/api/settings", wantCT: "application/json",
			wantBody: map[string]any{"tmdbKey": ""},
		},
		{
			name:       "devices",
			resp:       `[{"id":"d1","name":"Living Room TV","model":"Chromecast","addr":"10.0.0.5","port":8009}]`,
			do:         func(c *Client) error { _, err := c.Devices(ctx); return err },
			wantMethod: http.MethodGet, wantPath: "/api/cast/devices", wantNoBody: true,
		},
		{
			name:       "status escapes device",
			resp:       `{"state":"PLAYING","position":12.5}`,
			do:         func(c *Client) error { _, err := c.Status(ctx, "Living Room TV"); return err },
			wantMethod: http.MethodGet, wantPath: "/api/cast/Living%20Room%20TV/status", wantNoBody: true,
		},
		{
			name: "play",
			resp: `{"state":"LOADING"}`,
			do: func(c *Client) error {
				_, err := c.Play(ctx, "Living Room TV", PlayRequest{
					ID: "ab12", Audio: 1, Sub: "2", Start: 90, Mode: "auto", Resume: "continue",
				})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/api/cast/Living%20Room%20TV/play", wantCT: "application/json",
			wantBody: map[string]any{
				"id": "ab12", "audio": float64(1), "sub": "2", "start": float64(90),
				"mode": "auto", "resume": "continue",
			},
		},
		{
			name:       "control pause without value",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.Control(ctx, "Living Room TV", "pause", nil) },
			wantMethod: http.MethodPost, wantPath: "/api/cast/Living%20Room%20TV/control", wantCT: "application/json",
			wantBody: map[string]any{"action": "pause"},
		},
		{
			name:       "control volume with fractional value",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.Control(ctx, "Living Room TV", "volume", 0.5) },
			wantMethod: http.MethodPost, wantPath: "/api/cast/Living%20Room%20TV/control", wantCT: "application/json",
			wantBody: map[string]any{"action": "volume", "value": 0.5},
		},
		{
			name:       "control seek with integer value",
			resp:       `{"ok":true}`,
			do:         func(c *Client) error { return c.Control(ctx, "TV", "seek", 300) },
			wantMethod: http.MethodPost, wantPath: "/api/cast/TV/control", wantCT: "application/json",
			wantBody: map[string]any{"action": "seek", "value": float64(300)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, get := fakeServer(t, http.StatusOK, tc.resp)
			if err := tc.do(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			rec := get()
			if rec.method != tc.wantMethod {
				t.Errorf("method = %q, want %q", rec.method, tc.wantMethod)
			}
			if rec.escapedPath != tc.wantPath {
				t.Errorf("path = %q, want %q", rec.escapedPath, tc.wantPath)
			}
			if rec.query != tc.wantQuery {
				t.Errorf("query = %q, want %q", rec.query, tc.wantQuery)
			}
			if rec.contentType != tc.wantCT {
				t.Errorf("Content-Type = %q, want %q", rec.contentType, tc.wantCT)
			}
			if tc.wantNoBody {
				if rec.body != "" {
					t.Errorf("body = %q, want none", rec.body)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(rec.body), &got); err != nil {
				t.Fatalf("body %q is not a JSON object: %v", rec.body, err)
			}
			if !reflect.DeepEqual(got, tc.wantBody) {
				t.Errorf("body = %v, want %v", got, tc.wantBody)
			}
			if tc.name == "control pause without value" {
				if _, ok := got["value"]; ok {
					t.Errorf("body has a value key, want none")
				}
			}
		})
	}
}

func TestDecodedResponses(t *testing.T) {
	c, _ := fakeServer(t, http.StatusOK, `{"state":"PAUSED","position":42,"duration":3600,"volume":0.3,"muted":true}`)
	st, err := c.Status(context.Background(), "TV")
	if err != nil {
		t.Fatal(err)
	}
	want := CastStatus{State: "PAUSED", Position: 42, Duration: 3600, Volume: 0.3, Muted: true}
	if st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}

	c, _ = fakeServer(t, http.StatusOK, `{"name":"box","port":8080,"tmdb":true}`)
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "box" || info.Port != 8080 || !info.TMDB {
		t.Errorf("info = %+v", info)
	}
}

func TestPlayResumeError(t *testing.T) {
	c, _ := fakeServer(t, http.StatusConflict,
		`{"code":"resume","position":1500,"duration":5400,"error":"already watched"}`)
	_, err := c.Play(context.Background(), "TV", PlayRequest{ID: "x"})
	var re *ResumeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResumeError", err, err)
	}
	if re.Position != 1500 || re.Duration != 5400 {
		t.Errorf("ResumeError = %+v, want position 1500 duration 5400", re)
	}
	want := "already watched up to 25:00 (choose continue or restart)"
	if re.Error() != want {
		t.Errorf("Error() = %q, want %q", re.Error(), want)
	}
}

func TestPlayConflictWithoutResumeCodeIsAPIError(t *testing.T) {
	c, _ := fakeServer(t, http.StatusConflict, `{"error":"busy"}`)
	_, err := c.Play(context.Background(), "TV", PlayRequest{ID: "x"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusConflict || api.Message != "busy" {
		t.Fatalf("err = %v, want *APIError{409, busy}", err)
	}
}

func TestResumeErrorClock(t *testing.T) {
	tests := []struct {
		pos  float64
		want string
	}{
		{0, "already watched up to 0:00 (choose continue or restart)"},
		{59.9, "already watched up to 0:59 (choose continue or restart)"},
		{3599, "already watched up to 59:59 (choose continue or restart)"},
		{3600, "already watched up to 1:00:00 (choose continue or restart)"},
		{3725, "already watched up to 1:02:05 (choose continue or restart)"},
	}
	for _, tc := range tests {
		if got := (&ResumeError{Position: tc.pos}).Error(); got != tc.want {
			t.Errorf("pos %v: Error() = %q, want %q", tc.pos, got, tc.want)
		}
	}
}

func TestAPIErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		resp    string
		wantMsg string
	}{
		{"404 json error", http.StatusNotFound, `{"error":"no such item"}`, "no such item"},
		{"400 json error", http.StatusBadRequest, `{"error":"bad body"}`, "bad body"},
		{"non-json body trimmed", http.StatusBadGateway, "  upstream down\n", "upstream down"},
		{"empty body uses status text", http.StatusInternalServerError, "", "Internal Server Error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fakeServer(t, tc.status, tc.resp)
			_, err := c.Library(context.Background())
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %v (%T), want *APIError", err, err)
			}
			if api.Status != tc.status || api.Message != tc.wantMsg {
				t.Errorf("APIError = %+v, want status %d message %q", api, tc.status, tc.wantMsg)
			}
			want := fmt.Sprintf("mediad: %s (HTTP %d)", tc.wantMsg, tc.status)
			if api.Error() != want {
				t.Errorf("Error() = %q, want %q", api.Error(), want)
			}
		})
	}
}

func TestArtURL(t *testing.T) {
	c := New("http://host:9000/")
	tests := []struct {
		name string
		rel  *string
		want string
	}{
		{"nil", nil, ""},
		{"empty", strp(""), ""},
		{"relative with query", strp("/art/ab12/poster?v=123"), "http://host:9000/art/ab12/poster?v=123"},
		{"relative without leading slash", strp("art/ab12/poster"), "http://host:9000/art/ab12/poster"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.ArtURL(tc.rel); got != tc.want {
				t.Errorf("ArtURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchArt(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		resp     string
		wantData string
		wantErr  bool
	}{
		{"success", http.StatusOK, "JPEGDATA", "JPEGDATA", false},
		{"not found", http.StatusNotFound, `{"error":"no art"}`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, get := recordingServer(t, tc.status, tc.resp)
			c := New(srv.URL)
			data, err := c.FetchArt(context.Background(), strp("/art/ab12/poster?v=1"))
			rec := get()
			if rec.method != http.MethodGet || rec.escapedPath != "/art/ab12/poster" || rec.query != "v=1" {
				t.Errorf("request = %s %s?%s, want GET /art/ab12/poster?v=1", rec.method, rec.escapedPath, rec.query)
			}
			if tc.wantErr {
				var api *APIError
				if !errors.As(err, &api) || api.Status != tc.status || api.Message != "no art" {
					t.Fatalf("err = %v, want *APIError{404, no art}", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(data) != tc.wantData {
				t.Errorf("data = %q, want %q", data, tc.wantData)
			}
		})
	}
}

func TestFetchArtNilPathErrors(t *testing.T) {
	c := New("http://127.0.0.1:1")
	if _, err := c.FetchArt(context.Background(), nil); err == nil {
		t.Error("FetchArt(nil) returned no error")
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	if got := New("http://host:9000///").Base; got != "http://host:9000" {
		t.Errorf("Base = %q, want %q", got, "http://host:9000")
	}
}

func TestLogs(t *testing.T) {
	srv, last := recordingServer(t, 200, `{"entries":[{"seq":4,"time":"2026-10-17T10:00:00Z","level":"INFO","msg":"hi"}],"last":4}`)
	c := New(srv.URL)
	entries, newest, err := c.Logs(context.Background(), 3)
	if err != nil || newest != 4 || len(entries) != 1 || entries[0].Msg != "hi" {
		t.Fatalf("got %+v %d %v", entries, newest, err)
	}
	if r := last(); r.escapedPath != "/api/logs" || r.query != "after=3" {
		t.Errorf("request = %+v", r)
	}
}
