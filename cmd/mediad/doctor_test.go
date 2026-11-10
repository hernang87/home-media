package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDoctorServer(t *testing.T) {
	tests := []struct {
		name    string
		info    string // body of /api/info; "" answers 500
		devices string // body of /api/cast/devices; "" answers 500
		want    []string
	}{
		{"running with a TV", `{"name":"Den","version":"1.2.3"}`, `[{"id":"a","name":"Living Room"},{"id":"b","name":"Bedroom"}]`,
			[]string{"ok   server is running as Den v1.2.3", "ok   Chromecasts: Living Room, Bedroom"}},
		{"running without TVs", `{"name":"Den","version":"1.2.3"}`, `[]`,
			[]string{"ok   server is running as Den v1.2.3", "warn no Chromecasts found"}},
		{"devices fail", `{"name":"Den","version":"1.2.3"}`, "",
			[]string{"ok   server is running", "warn could not list Chromecasts: HTTP 500"}},
		{"something else on the port", "", "", []string{"warn server not running on port 8200"}},
		{"not JSON", "<html>", "", []string{"warn server not running on port 8200"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer := func(body string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if body == "" {
						http.Error(w, "no", http.StatusInternalServerError)
						return
					}
					io.WriteString(w, body)
				}
			}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/info", answer(tt.info))
			mux.HandleFunc("GET /api/cast/devices", answer(tt.devices))
			srv := httptest.NewServer(mux)
			defer srv.Close()

			var out bytes.Buffer
			line := func(tag, format string, args ...any) {
				fmt.Fprintf(&out, "%-4s %s\n", tag, fmt.Sprintf(format, args...))
			}
			doctorServer(line, srv.URL, 8200)
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output %q lacks %q", out.String(), want)
				}
			}
			if strings.Contains(out.String(), "FAIL") {
				t.Errorf("a server problem is never a failure: %q", out.String())
			}
		})
	}
}

// freePort returns a port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRunDoctorWithoutServer(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s missing", bin)
		}
	}
	media := t.TempDir()
	port := freePort(t)
	t.Chdir(t.TempDir()) // config.Load reads ./.env and creates ./data
	t.Setenv("MEDIA_DIR", media)
	t.Setenv("PORT", strconv.Itoa(port))
	t.Setenv("TMDB_API_KEY", "")

	var out bytes.Buffer
	if err := runDoctor(&out); err != nil {
		t.Fatalf("runDoctor = %v\n%s", err, out.String())
	}
	resolved, _ := filepath.EvalSymlinks(media)
	for _, want := range []string{
		"ok   ffmpeg found", "ok   ffprobe found",
		fmt.Sprintf("ok   port: %d", port),
		"warn no TMDB key",
		fmt.Sprintf("warn server not running on port %d", port),
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "media dir: "+media) && !strings.Contains(out.String(), "media dir: "+resolved) {
		t.Errorf("output lacks the media dir %s:\n%s", media, out.String())
	}

	t.Setenv("TMDB_API_KEY", "abcdef0123456789abcdef0123456789")
	out.Reset()
	if err := runDoctor(&out); err != nil || !strings.Contains(out.String(), "ok   TMDB_API_KEY is set") {
		t.Errorf("with a key: %v\n%s", err, out.String())
	}
}

func TestRunDoctorFailsWithoutFFmpeg(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MEDIA_DIR", t.TempDir())
	t.Setenv("PORT", strconv.Itoa(freePort(t)))
	t.Setenv("PATH", t.TempDir()) // nothing is on PATH
	var out bytes.Buffer
	if err := runDoctor(&out); err == nil {
		t.Errorf("want an error when ffmpeg is missing:\n%s", out.String())
	}
	for _, want := range []string{"FAIL ffmpeg not found", "FAIL ffprobe not found"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if doctorResult(false) != nil || checkBinary("definitely-not-a-binary") == nil {
		t.Error("doctorResult(false) must be nil and a missing binary an error")
	}
}

func TestSettingsViewAndPlistPath(t *testing.T) {
	h := newSettingsHarness(t)
	h.overrides[envTMDBLang] = "es-AR"
	v := h.s.Settings()
	if v.MediaDir != h.media || v.Port != 8200 || !v.TMDBKeySet || v.TMDBLanguage != "en-US" || v.RestartRequired {
		t.Errorf("settings view = %+v", v)
	}
	if len(v.Overridden) != 1 || v.Overridden[0] != envTMDBLang {
		t.Errorf("overridden = %v, want only %s", v.Overridden, envTMDBLang)
	}
	assertNoKeyInView(t, v, oldTestKey)

	if got := plistPathIn("/tmp/agents"); got != "/tmp/agents/com.homemedia.mediad.plist" {
		t.Errorf("plistPathIn = %s", got)
	}
	if got := agentDomain(); !strings.HasPrefix(got, "gui/") {
		t.Errorf("agentDomain = %s", got)
	}
}
