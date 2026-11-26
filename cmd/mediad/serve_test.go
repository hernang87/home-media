package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homemedia/internal/config"
	"homemedia/internal/logbuf"
	"homemedia/internal/model"
)

// startServe runs serve on a random loopback port over media and returns its
// base URL, its data dir and a stop function that returns serve's error.
func startServe(t *testing.T, media string) (base, data string, stop func() error) {
	t.Helper()
	data = t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		MediaDir:     media,
		Port:         ln.Addr().(*net.TCPAddr).Port,
		ServerName:   "Test Server",
		TMDBLanguage: "en-US",
		DataDir:      data,
		ArtDir:       filepath.Join(data, "art"),
		SubDir:       filepath.Join(data, "subs"),
		KeyDir:       filepath.Join(data, "keyframes"),
		TmpDir:       filepath.Join(t.TempDir(), "hls"),
	}
	if err := os.MkdirAll(cfg.ArtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, cfg, logbuf.New(100), ln, false) }()
	var once bool
	var serveErr error
	stop = func() error {
		if !once {
			once = true
			cancel()
			select {
			case serveErr = <-errc:
			case <-time.After(10 * time.Second):
				t.Fatal("serve did not return after cancel")
			}
		}
		return serveErr
	}
	t.Cleanup(func() { _ = stop() })
	return "http://" + ln.Addr().String(), data, stop
}

func getJSONT(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
}

// waitFor polls cond every 20 ms for up to 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServeAnswersAndShutsDown(t *testing.T) {
	base, _, stop := startServe(t, t.TempDir())

	var info model.ServerInfo
	getJSONT(t, base+"/api/info", &info)
	if info.Name != "Test Server" || info.Version != version {
		t.Errorf("info = %+v, want name Test Server, version %s", info, version)
	}

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Errorf("GET / = %s, %.60q", resp.Status, body)
	}

	if err := stop(); err != nil {
		t.Fatalf("serve returned %v after cancel", err)
	}
	if _, err := http.Get(base + "/api/info"); err == nil {
		t.Error("server still answering after stop")
	}
}

func TestServeScansAndSavesLibrary(t *testing.T) {
	fixture, _ := filepath.Abs("../../testdata/fixtures/Shows/Test Show/Season 1/Test.Show.S01E01.mkv")
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Skip("run testdata/gen.sh first")
	}
	media := t.TempDir()
	if err := os.WriteFile(filepath.Join(media, "Some.Movie.2020.mkv"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	base, data, stop := startServe(t, media)

	waitFor(t, "the scan", func() bool {
		var snap model.Snapshot
		getJSONT(t, base+"/api/library", &snap)
		return len(snap.Items) == 1 && !snap.Status.Scanning
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	// Shutdown flushes the library, so the next start has it without a scan.
	b, err := os.ReadFile(filepath.Join(data, "library.json"))
	if err != nil {
		t.Fatalf("library.json not written: %v", err)
	}
	if !strings.Contains(string(b), "Some.Movie.2020.mkv") {
		t.Errorf("library.json does not list the movie: %.200s", b)
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // Serve fails at once on a closed listener
	data := t.TempDir()
	cfg := &config.Config{MediaDir: t.TempDir(), DataDir: data, ArtDir: filepath.Join(data, "art"), TmpDir: filepath.Join(data, "hls")}
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), cfg, logbuf.New(10), ln, false) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve on a closed listener returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return on a closed listener")
	}
}
