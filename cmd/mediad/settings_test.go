package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homemedia/internal/api"
	"homemedia/internal/metadata"
)

const (
	testKey    = "abcdef0123456789abcdef0123456789"
	seedEnv    = "# keep me\nPORT=8200\n"
	oldTestKey = "oldkey"
)

// settingsHarness is a settingsStore wired to temp files and recording fakes.
type settingsHarness struct {
	t         *testing.T
	s         *settingsStore
	envPath   string
	media     string
	meta      *metadata.Client
	checks    []string
	rescans   []bool
	checkErr  error
	overrides map[string]string
}

func newSettingsHarness(t *testing.T) *settingsHarness {
	t.Helper()
	h := &settingsHarness{
		t:         t,
		envPath:   filepath.Join(t.TempDir(), ".env"),
		media:     t.TempDir(),
		meta:      &metadata.Client{},
		overrides: map[string]string{},
	}
	h.meta.SetCredentials(oldTestKey, "en-US")
	if err := os.WriteFile(h.envPath, []byte(seedEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	h.s = &settingsStore{
		envPath:  h.envPath,
		port:     8200,
		mediaDir: h.media,
		meta:     h.meta,
		check: func(_ context.Context, key string) error {
			h.checks = append(h.checks, key)
			return h.checkErr
		},
		rescan: func(refresh bool) { h.rescans = append(h.rescans, refresh) },
		lookup: func(name string) (string, bool) {
			v, ok := h.overrides[name]
			return v, ok
		},
	}
	return h
}

func (h *settingsHarness) update(u api.SettingsUpdate) (api.SettingsView, error) {
	h.t.Helper()
	return h.s.Update(context.Background(), u)
}

func (h *settingsHarness) fileText() string {
	h.t.Helper()
	b, err := os.ReadFile(h.envPath)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *settingsHarness) assertFileUnchanged() {
	h.t.Helper()
	if got := h.fileText(); got != seedEnv {
		h.t.Errorf(".env changed: %q", got)
	}
}

func (h *settingsHarness) assertCredentials(wantKey, wantLang string) {
	h.t.Helper()
	key, lang := h.meta.Credentials()
	if key != wantKey || lang != wantLang {
		h.t.Errorf("credentials = (%q, %q), want (%q, %q)", key, lang, wantKey, wantLang)
	}
}

func strPtr(s string) *string { return &s }

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	var inv *api.InvalidSettingError
	if !errors.As(err, &inv) {
		t.Fatalf("err = %v, want *api.InvalidSettingError", err)
	}
}

func assertNoKeyInView(t *testing.T, v api.SettingsView, key string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), key) {
		t.Errorf("settings view leaks key: %s", b)
	}
}

func TestSettingsTMDBKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		checkErr    error
		wantInvalid bool
	}{
		{name: "accepted key is saved", key: testKey},
		{name: "rejected key changes nothing", key: testKey, checkErr: errors.New("401 unauthorized"), wantInvalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSettingsHarness(t)
			h.checkErr = tt.checkErr
			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(prev)

			v, err := h.update(api.SettingsUpdate{TMDBKey: strPtr(tt.key)})
			if len(h.checks) != 1 || h.checks[0] != tt.key {
				t.Errorf("check calls = %v, want one call with the key", h.checks)
			}
			if tt.wantInvalid {
				assertInvalid(t, err)
				h.assertFileUnchanged()
				h.assertCredentials(oldTestKey, "en-US")
				if len(h.rescans) != 0 {
					t.Errorf("rescans = %v, want none", h.rescans)
				}
				if !v.TMDBKeySet {
					t.Errorf("TMDBKeySet = false, the old key should still be set")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := "TMDB_API_KEY=" + tt.key + "\n"; !strings.Contains(h.fileText(), want) {
				t.Errorf(".env = %q, want line %q", h.fileText(), want)
			}
			h.assertCredentials(tt.key, "en-US")
			if len(h.rescans) != 1 || h.rescans[0] != false {
				t.Errorf("rescans = %v, want [false]", h.rescans)
			}
			if !v.TMDBKeySet {
				t.Errorf("TMDBKeySet = false, want true")
			}
			assertNoKeyInView(t, v, tt.key)
			if strings.Contains(logs.String(), tt.key) {
				t.Errorf("log leaks key: %s", logs.String())
			}
		})
	}
}

func TestSettingsLanguage(t *testing.T) {
	tests := []struct {
		name       string
		lang       string
		startLang  string
		wantErr    bool
		wantLang   string
		wantRescan []bool
	}{
		{name: "new language refetches titles", lang: "es-AR", wantLang: "es-AR", wantRescan: []bool{true}},
		{name: "invalid language is rejected", lang: "english!", wantErr: true, wantLang: "en-US"},
		{name: "empty language means en-US", lang: "  ", startLang: "es-AR", wantLang: "en-US", wantRescan: []bool{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSettingsHarness(t)
			if tt.startLang != "" {
				h.meta.SetCredentials(oldTestKey, tt.startLang)
			}
			v, err := h.update(api.SettingsUpdate{TMDBLanguage: strPtr(tt.lang)})
			if tt.wantErr {
				assertInvalid(t, err)
				h.assertFileUnchanged()
				h.assertCredentials(oldTestKey, tt.wantLang)
				if len(h.rescans) != 0 {
					t.Errorf("rescans = %v, want none", h.rescans)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := "TMDB_LANGUAGE=" + tt.wantLang + "\n"; !strings.Contains(h.fileText(), want) {
				t.Errorf(".env = %q, want line %q", h.fileText(), want)
			}
			h.assertCredentials(oldTestKey, tt.wantLang)
			if len(h.rescans) != len(tt.wantRescan) || (len(h.rescans) == 1 && h.rescans[0] != tt.wantRescan[0]) {
				t.Errorf("rescans = %v, want %v", h.rescans, tt.wantRescan)
			}
			if v.TMDBLanguage != tt.wantLang {
				t.Errorf("view language = %q, want %q", v.TMDBLanguage, tt.wantLang)
			}
			if len(h.checks) != 0 {
				t.Errorf("check called for a language change: %v", h.checks)
			}
		})
	}
}

func TestSettingsRemoveKey(t *testing.T) {
	h := newSettingsHarness(t)
	v, err := h.update(api.SettingsUpdate{TMDBKey: strPtr("")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "TMDB_API_KEY=\n"; !strings.HasSuffix(h.fileText(), want) {
		t.Errorf(".env = %q, want it to end with %q", h.fileText(), want)
	}
	if len(h.checks) != 0 {
		t.Errorf("check called on removal: %v", h.checks)
	}
	if len(h.rescans) != 0 {
		t.Errorf("rescans = %v, want none", h.rescans)
	}
	h.assertCredentials("", "en-US")
	if v.TMDBKeySet {
		t.Errorf("TMDBKeySet = true after removal")
	}
}

func TestSettingsMediaDir(t *testing.T) {
	h := newSettingsHarness(t)
	other := t.TempDir()

	v, err := h.update(api.SettingsUpdate{MediaDir: strPtr(other)})
	if err != nil {
		t.Fatal(err)
	}
	if want := "MEDIA_DIR=" + other + "\n"; !strings.Contains(h.fileText(), want) {
		t.Errorf(".env = %q, want line %q", h.fileText(), want)
	}
	if !v.RestartRequired {
		t.Errorf("RestartRequired = false, want true")
	}
	if v.MediaDir != h.media {
		t.Errorf("view MediaDir = %q, want the running dir %q", v.MediaDir, h.media)
	}
	if len(h.rescans) != 0 || len(h.checks) != 0 {
		t.Errorf("media dir change triggered rescans=%v checks=%v", h.rescans, h.checks)
	}

	v, err = h.update(api.SettingsUpdate{MediaDir: strPtr(h.media)})
	if err != nil {
		t.Fatal(err)
	}
	if want := "MEDIA_DIR=" + h.media + "\n"; !strings.Contains(h.fileText(), want) {
		t.Errorf(".env = %q, want line %q", h.fileText(), want)
	}
	if v.RestartRequired {
		t.Errorf("RestartRequired = true after switching back to the running dir")
	}

	missing := filepath.Join(h.media, "does-not-exist")
	if _, err := h.update(api.SettingsUpdate{MediaDir: strPtr(missing)}); err == nil {
		t.Fatal("missing folder accepted")
	} else {
		assertInvalid(t, err)
	}
	if strings.Contains(h.fileText(), missing) {
		t.Errorf("missing folder written to .env: %q", h.fileText())
	}
}

func TestSettingsEnvOverride(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		update api.SettingsUpdate
	}{
		{name: "TMDB key", envKey: "TMDB_API_KEY", update: api.SettingsUpdate{TMDBKey: strPtr(testKey)}},
		{name: "language", envKey: "TMDB_LANGUAGE", update: api.SettingsUpdate{TMDBLanguage: strPtr("es-AR")}},
		{name: "media folder", envKey: "MEDIA_DIR", update: api.SettingsUpdate{MediaDir: strPtr(t.TempDir())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSettingsHarness(t)
			h.overrides[tt.envKey] = "from-env"
			v, err := h.update(tt.update)
			assertInvalid(t, err)
			if !containsString(v.Overridden, tt.envKey) {
				t.Errorf("Overridden = %v, want it to contain %s", v.Overridden, tt.envKey)
			}
			h.assertFileUnchanged()
			h.assertCredentials(oldTestKey, "en-US")
			if len(h.checks) != 0 || len(h.rescans) != 0 {
				t.Errorf("checks=%v rescans=%v, want none", h.checks, h.rescans)
			}
		})
	}
}

func TestSettingsNoOpWritesNothing(t *testing.T) {
	h := newSettingsHarness(t)
	// An old mtime lets us see whether the file was rewritten at all.
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(h.envPath, old, old); err != nil {
		t.Fatal(err)
	}
	_, err := h.update(api.SettingsUpdate{
		TMDBKey:      strPtr(oldTestKey),
		TMDBLanguage: strPtr("en-US"),
		MediaDir:     strPtr(h.media),
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(h.envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Errorf(".env was rewritten by a no-op update (mtime %v)", st.ModTime())
	}
	h.assertFileUnchanged()
	if len(h.checks) != 0 || len(h.rescans) != 0 {
		t.Errorf("checks=%v rescans=%v, want none", h.checks, h.rescans)
	}
}

func TestSettingsMalformedKeyRejected(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "space", key: "abc def"},
		{name: "double quote", key: `abc"def`},
		{name: "single quote", key: "abc'def"},
		{name: "newline inside", key: "abc\ndef"},
		{name: "carriage return inside", key: "abc\rdef"},
		{name: "tab", key: "abc\tdef"},
		{name: "NUL", key: "abc\x00def"},
		{name: "too long", key: strings.Repeat("a", 513)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSettingsHarness(t)
			_, err := h.update(api.SettingsUpdate{TMDBKey: strPtr(tt.key)})
			assertInvalid(t, err)
			if len(h.checks) != 0 {
				t.Errorf("malformed key was sent to TMDB: %v", h.checks)
			}
			h.assertFileUnchanged()
			h.assertCredentials(oldTestKey, "en-US")
			if len(h.rescans) != 0 {
				t.Errorf("rescans = %v, want none", h.rescans)
			}
		})
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
