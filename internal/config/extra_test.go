package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv unsets keys for the duration of the test (t.Setenv restores them).
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// isolate points HOME and the working directory at fresh temp dirs and clears
// the config-related env vars.
func isolate(t *testing.T) (cwd, home string) {
	t.Helper()
	clearEnv(t, "MEDIA_DIR", "PORT", "SERVER_NAME", "TMDB_API_KEY", "TMDB_LANGUAGE")
	home = t.TempDir()
	t.Setenv("HOME", home)
	cwd = t.TempDir()
	t.Chdir(cwd)
	return cwd, home
}

func TestDotEnvParsing(t *testing.T) {
	cwd, _ := isolate(t)
	env := `# comment line
   # indented comment

TMDB_API_KEY="quoted key"
TMDB_LANGUAGE='es-AR'
SERVER_NAME = Living Room
PORT=  9100
`
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.TMDBAPIKey != "quoted key" {
		t.Errorf("TMDBAPIKey = %q, want %q", c.TMDBAPIKey, "quoted key")
	}
	if c.TMDBLanguage != "es-AR" {
		t.Errorf("TMDBLanguage = %q, want es-AR", c.TMDBLanguage)
	}
	if c.ServerName != "Living Room" {
		t.Errorf("ServerName = %q, want %q", c.ServerName, "Living Room")
	}
	if c.Port != 9100 {
		t.Errorf("Port = %d, want 9100", c.Port)
	}
}

func TestDotEnvFirstKeyWins(t *testing.T) {
	cwd, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("PORT=9010\nPORT=9020\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9010 {
		t.Errorf("Port = %d, want 9010", c.Port)
	}
}

func TestEnvPrecedence(t *testing.T) {
	cwd, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("PORT=9001\nSERVER_NAME=FromFile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "9002")
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9002 {
		t.Errorf("Port = %d, want 9002 (real env beats .env)", c.Port)
	}
	if c.ServerName != "FromFile" {
		t.Errorf("ServerName = %q, want FromFile (.env used when env unset)", c.ServerName)
	}
}

func TestEmptyRealEnvBeatsDotEnv(t *testing.T) {
	cwd, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("SERVER_NAME=FromFile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVER_NAME", "")
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerName != "Home Media" {
		t.Errorf("ServerName = %q, want default when real env is set but empty", c.ServerName)
	}
}

func TestTildeExpansion(t *testing.T) {
	_, home := isolate(t)
	movies := filepath.Join(home, "Movies")
	if err := os.Mkdir(movies, 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := Load("~/Movies")
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaDir != movies {
		t.Errorf("MediaDir = %q, want %q", c.MediaDir, movies)
	}

	t.Setenv("MEDIA_DIR", "~/Movies")
	c, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaDir != movies {
		t.Errorf("MEDIA_DIR ~ expansion: MediaDir = %q, want %q", c.MediaDir, movies)
	}
}

func TestMediaCandidateOrder(t *testing.T) {
	_, home := isolate(t)
	bajadas := filepath.Join(home, "Bajadas")
	if err := os.Mkdir(bajadas, 0o755); err != nil {
		t.Fatal(err)
	}

	// No arg, no MEDIA_DIR: falls back to ~/Bajadas.
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaDir != bajadas {
		t.Errorf("MediaDir = %q, want %q", c.MediaDir, bajadas)
	}

	// Missing arg is skipped in favour of MEDIA_DIR.
	envDir := t.TempDir()
	t.Setenv("MEDIA_DIR", envDir)
	c, err = Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaDir != envDir {
		t.Errorf("MediaDir = %q, want MEDIA_DIR %q", c.MediaDir, envDir)
	}
}

func TestMissingMediaDirError(t *testing.T) {
	isolate(t)
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := Load(missing)
	if err == nil {
		t.Fatal("Load with no media dir: want error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"no media directory", missing, "MEDIA_DIR", "Bajadas"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestPortFallback(t *testing.T) {
	cases := map[string]int{
		"":      8200,
		"abc":   8200,
		"0":     8200,
		"-1":    8200,
		"70000": 8200,
		"9000":  9000,
	}
	for in, want := range cases {
		t.Run("PORT="+in, func(t *testing.T) {
			isolate(t)
			t.Setenv("PORT", in)
			c, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if c.Port != want {
				t.Errorf("Port = %d, want %d", c.Port, want)
			}
		})
	}
}

func TestDirectoriesCreated(t *testing.T) {
	cwd, _ := isolate(t)
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "data" || c.ArtDir != filepath.Join("data", "art") {
		t.Errorf("DataDir=%q ArtDir=%q", c.DataDir, c.ArtDir)
	}
	for _, d := range []string{c.DataDir, c.ArtDir} {
		if st, err := os.Stat(filepath.Join(cwd, d)); err != nil || !st.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
	if c.TmpDir != filepath.Join(os.TempDir(), "home-media-hls") {
		t.Errorf("TmpDir = %q", c.TmpDir)
	}
}
