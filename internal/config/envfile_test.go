package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		before  *string // nil = no file
		updates map[string]string
		want    string
	}{
		{
			name:    "creates the file",
			updates: map[string]string{"TMDB_API_KEY": "abc"},
			want:    "TMDB_API_KEY=abc\n",
		},
		{
			name:    "keeps comments and unrelated keys, replaces in place",
			before:  ptr("# note\nTMDB_API_KEY=old\nPORT=9000\n\nMEDIA_DIR=/a b\n"),
			updates: map[string]string{"TMDB_API_KEY": "new"},
			want:    "# note\nTMDB_API_KEY=new\nPORT=9000\n\nMEDIA_DIR=/a b\n",
		},
		{
			name:    "replaces only the first occurrence, the one Load uses",
			before:  ptr("TMDB_API_KEY=first\nTMDB_API_KEY=second\n"),
			updates: map[string]string{"TMDB_API_KEY": "new"},
			want:    "TMDB_API_KEY=new\nTMDB_API_KEY=second\n",
		},
		{
			name:    "a commented example does not count as the key",
			before:  ptr("# TMDB_API_KEY=example\n"),
			updates: map[string]string{"TMDB_API_KEY": "real"},
			want:    "# TMDB_API_KEY=example\nTMDB_API_KEY=real\n",
		},
		{
			name:    "appends missing keys in sorted order",
			before:  ptr("PORT=1\n"),
			updates: map[string]string{"TMDB_LANGUAGE": "es-AR", "MEDIA_DIR": "/m"},
			want:    "PORT=1\nMEDIA_DIR=/m\nTMDB_LANGUAGE=es-AR\n",
		},
		{
			name:    "empty value clears but keeps the line",
			before:  ptr("TMDB_API_KEY=old\n"),
			updates: map[string]string{"TMDB_API_KEY": ""},
			want:    "TMDB_API_KEY=\n",
		},
		{
			name:    "adds a missing final newline and handles CRLF",
			before:  ptr("A=1\r\nB=2"),
			updates: map[string]string{"B": "3"},
			want:    "A=1\nB=3\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if tt.before != nil {
				if err := os.WriteFile(path, []byte(*tt.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := UpdateEnvFile(path, tt.updates); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
			st, _ := os.Stat(path)
			if st.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want 0600", st.Mode().Perm())
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Errorf("leftover files in dir: %v", entries)
			}
		})
	}
}

func TestUpdateEnvFileRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "A=1\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, updates := range map[string]map[string]string{
		"newline injects a key": {"TMDB_API_KEY": "x\nMEDIA_DIR=/evil"},
		"carriage return":       {"TMDB_API_KEY": "x\rMEDIA_DIR=/evil"},
		"bad key name":          {"not a key": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := UpdateEnvFile(path, updates); err == nil {
				t.Fatal("want error")
			}
			got, _ := os.ReadFile(path)
			if string(got) != orig {
				t.Errorf("file changed to %q", got)
			}
		})
	}
}

// Whatever UpdateEnvFile writes, parseDotEnv must read back unchanged.
func TestUpdateEnvFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	want := map[string]string{"MEDIA_DIR": "/Users/me/My Movies", "TMDB_API_KEY": strings.Repeat("a", 32), "TMDB_LANGUAGE": "es-AR"}
	if err := UpdateEnvFile(path, want); err != nil {
		t.Fatal(err)
	}
	got := parseDotEnv(path)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func ptr(s string) *string { return &s }
