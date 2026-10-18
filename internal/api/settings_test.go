package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homemedia/internal/config"
)

const (
	loopbackRemote = "127.0.0.1:1234"
	otherLANRemote = "192.168.1.77:5000"
	settingsHost   = "127.0.0.1:8200"
)

// fakeSettings records the last update and returns a configurable error.
type fakeSettings struct {
	view   SettingsView
	err    error
	called bool
	last   SettingsUpdate
}

func (f *fakeSettings) Settings() SettingsView { return f.view }

func (f *fakeSettings) Update(_ context.Context, u SettingsUpdate) (SettingsView, error) {
	f.called = true
	f.last = u
	if f.err != nil {
		return f.view, f.err
	}
	if u.TMDBKey != nil {
		f.view.TMDBKeySet = *u.TMDBKey != ""
	}
	return f.view, nil
}

func settingsRequest(method, remote, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
	req.RemoteAddr = remote
	req.Host = settingsHost
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// ownsAddress reports whether this machine has the IP addr on an interface.
func ownsAddress(t *testing.T, addr string) bool {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.String() == addr {
			return true
		}
	}
	return false
}

func TestSettingsGet(t *testing.T) {
	tests := []struct {
		name         string
		remote       string
		wantEditable bool
	}{
		{name: "loopback is editable", remote: loopbackRemote, wantEditable: true},
		{name: "other LAN host is read only", remote: otherLANRemote, wantEditable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.remote == otherLANRemote && ownsAddress(t, "192.168.1.77") {
				t.Skip("192.168.1.77 belongs to this machine")
			}
			fake := &fakeSettings{view: SettingsView{MediaDir: "/media", TMDBLanguage: "en-US", TMDBKeySet: true, Port: 8200}}
			h := New(Deps{Config: &config.Config{}, Settings: fake})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, settingsRequest(http.MethodGet, tt.remote, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["editable"] != tt.wantEditable {
				t.Errorf("editable = %v, want %v", got["editable"], tt.wantEditable)
			}
			if _, ok := got["tmdbKey"]; ok {
				t.Errorf("response has a tmdbKey field: %s", rec.Body)
			}
			if got["tmdbKeySet"] != true {
				t.Errorf("tmdbKeySet = %v, want true", got["tmdbKeySet"])
			}
		})
	}
}

func TestSettingsPost(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		origin     string
		body       string
		fakeErr    error
		wantStatus int
		wantCalled bool
		wantBody   string // substring of the response body
		notBody    string // must not appear in the response body
	}{
		{
			name:       "loopback saves the key",
			remote:     loopbackRemote,
			body:       `{"tmdbKey":"k"}`,
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "other LAN host is refused without calling the store",
			remote:     otherLANRemote,
			body:       `{"tmdbKey":"k"}`,
			wantStatus: http.StatusForbidden,
			wantBody:   `"error"`,
		},
		{
			name:       "rejected input is a 400 with the message",
			remote:     loopbackRemote,
			body:       `{"tmdbKey":"bad"}`,
			fakeErr:    &InvalidSettingError{Msg: "bad"},
			wantStatus: http.StatusBadRequest,
			wantCalled: true,
			wantBody:   `{"error":"bad"}`,
		},
		{
			name:       "internal failure is a 500 without the internal text",
			remote:     loopbackRemote,
			body:       `{"tmdbKey":"k"}`,
			fakeErr:    errors.New("disk full"),
			wantStatus: http.StatusInternalServerError,
			wantCalled: true,
			notBody:    "disk",
		},
		{
			name:       "body over 64 KB is a 400",
			remote:     loopbackRemote,
			body:       `{"tmdbKey":"` + strings.Repeat("a", 70<<10) + `"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cross-site origin is refused by apiGuard",
			remote:     loopbackRemote,
			origin:     "https://evil.example",
			body:       `{"tmdbKey":"k"}`,
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.remote == otherLANRemote && ownsAddress(t, "192.168.1.77") {
				t.Skip("192.168.1.77 belongs to this machine")
			}
			fake := &fakeSettings{err: tt.fakeErr}
			h := New(Deps{Config: &config.Config{}, Settings: fake})

			req := settingsRequest(http.MethodPost, tt.remote, tt.body)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if fake.called != tt.wantCalled {
				t.Errorf("store called = %v, want %v", fake.called, tt.wantCalled)
			}
			if tt.wantStatus == http.StatusOK && (fake.last.TMDBKey == nil || *fake.last.TMDBKey != "k") {
				t.Errorf("store got %+v, want TMDBKey k", fake.last)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to contain %s", rec.Body, tt.wantBody)
			}
			if tt.notBody != "" && strings.Contains(rec.Body.String(), tt.notBody) {
				t.Errorf("body leaks %q: %s", tt.notBody, rec.Body)
			}
		})
	}
}

func TestSettingsRouteAbsent(t *testing.T) {
	h := New(Deps{Config: &config.Config{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, settingsRequest(http.MethodGet, loopbackRemote, ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, settingsRequest(http.MethodPost, loopbackRemote, `{"tmdbKey":"k"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404", rec.Code)
	}
}

func TestInfoTMDBReflectsSettings(t *testing.T) {
	fake := &fakeSettings{view: SettingsView{TMDBKeySet: false}}
	h := New(Deps{Config: &config.Config{}, Settings: fake})

	tmdb := func() bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/info", nil)
		req.RemoteAddr = loopbackRemote
		req.Host = settingsHost
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		v, _ := info["tmdb"].(bool)
		return v
	}

	if tmdb() {
		t.Errorf("tmdb = true with no key set")
	}
	fake.view.TMDBKeySet = true
	if !tmdb() {
		t.Errorf("tmdb = false after the key was set")
	}
	fake.view.TMDBKeySet = false
	if tmdb() {
		t.Errorf("tmdb = true after the key was removed")
	}
}

func TestIsThisMachine(t *testing.T) {
	tests := []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:1", true},
		{"[::1]:1", true},
		{"[::ffff:127.0.0.1]:1", true},
		{"8.8.8.8:1", false},
		{"not-an-ip", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			if got := isThisMachine(tt.remote); got != tt.want {
				t.Errorf("isThisMachine(%q) = %v, want %v", tt.remote, got, tt.want)
			}
		})
	}
}
