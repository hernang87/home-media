package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	Routes(mux)
	tests := []struct {
		path, wantType, wantBody string
		want                     int
	}{
		{"/", "text/html", "<title>", http.StatusOK},
		{"/app/app.js", "text/javascript", "", http.StatusOK},
		{"/app/app.css", "text/css", "", http.StatusOK},
		{"/app/manifest.webmanifest", "", "\"start_url\"", http.StatusOK},
		{"/app/icon-512.png", "image/png", "", http.StatusOK},
		{"/app/nope.js", "", "", http.StatusNotFound},
		{"/other", "", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want != http.StatusOK {
				return
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), tt.wantType) {
				t.Errorf("content type = %q, want prefix %q", rec.Header().Get("Content-Type"), tt.wantType)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body missing %q", tt.wantBody)
			}
			if rec.Header().Get("Cache-Control") != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", rec.Header().Get("Cache-Control"))
			}
		})
	}
}
