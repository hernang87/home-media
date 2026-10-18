package vm

import (
	"reflect"
	"strings"
	"testing"

	"homemedia/internal/client"
)

func str(s string) *string { return &s }

func TestLanguageOptions(t *testing.T) {
	got := LanguageOptions("en-US")
	if got[0] != "en-US" || len(got) != len(commonLanguages) {
		t.Errorf("known language: %v", got)
	}
	got = LanguageOptions("sv-FI")
	if got[0] != "sv-FI" || len(got) != len(commonLanguages)+1 {
		t.Errorf("unknown language should be listed first: %v", got)
	}
	got[1] = "mutated"
	if commonLanguages[0] == "mutated" || LanguageOptions("")[1] == "mutated" {
		t.Error("LanguageOptions must not expose the shared slice")
	}
}

func TestBuildSettingsUpdate(t *testing.T) {
	base := client.Settings{MediaDir: "/media", TMDBLanguage: "en-US", TMDBKeySet: true, Editable: true}
	tests := []struct {
		name           string
		s              client.Settings
		dir, lang, key string
		removeKey      bool
		want           client.SettingsUpdate
		wantChanged    bool
	}{
		{name: "nothing changed", s: base, dir: "/media", lang: "en-US"},
		{name: "blank form fields are ignored", s: base},
		{name: "folder changed", s: base, dir: " /movies ", lang: "en-US",
			want: client.SettingsUpdate{MediaDir: str("/movies")}, wantChanged: true},
		{name: "language changed", s: base, dir: "/media", lang: "es-AR",
			want: client.SettingsUpdate{TMDBLanguage: str("es-AR")}, wantChanged: true},
		{name: "new key", s: base, dir: "/media", lang: "en-US", key: "  abc ",
			want: client.SettingsUpdate{TMDBKey: str("abc")}, wantChanged: true},
		{name: "remove key", s: base, dir: "/media", lang: "en-US", removeKey: true,
			want: client.SettingsUpdate{TMDBKey: str("")}, wantChanged: true},
		{name: "remove key when none is set is a no-op",
			s:   client.Settings{MediaDir: "/media", TMDBLanguage: "en-US", Editable: true},
			dir: "/media", lang: "en-US", removeKey: true},
		{name: "a typed key beats remove", s: base, dir: "/media", lang: "en-US", key: "abc", removeKey: true,
			want: client.SettingsUpdate{TMDBKey: str("abc")}, wantChanged: true},
		{name: "locked fields are never sent",
			s:   client.Settings{MediaDir: "/media", TMDBLanguage: "en-US", Overridden: []string{"MEDIA_DIR", "TMDB_API_KEY", "TMDB_LANGUAGE"}},
			dir: "/movies", lang: "es-AR", key: "abc"},
		{name: "only the unlocked field goes",
			s:   client.Settings{MediaDir: "/media", TMDBLanguage: "en-US", Overridden: []string{"TMDB_API_KEY"}},
			dir: "/movies", lang: "en-US", key: "abc",
			want: client.SettingsUpdate{MediaDir: str("/movies")}, wantChanged: true},
		{name: "everything at once", s: base, dir: "/movies", lang: "pt-BR", key: "k",
			want: client.SettingsUpdate{MediaDir: str("/movies"), TMDBLanguage: str("pt-BR"), TMDBKey: str("k")}, wantChanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := BuildSettingsUpdate(tt.s, tt.dir, tt.lang, tt.key, tt.removeKey)
			if changed != tt.wantChanged || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v changed=%v, want %+v changed=%v", got, changed, tt.want, tt.wantChanged)
			}
		})
	}
}

func TestSettingsNotes(t *testing.T) {
	if got := SettingsNotes(client.Settings{Editable: true}); len(got) != 0 {
		t.Errorf("clean settings: %v", got)
	}
	got := SettingsNotes(client.Settings{Overridden: []string{"MEDIA_DIR", "TMDB_API_KEY"}, RestartRequired: true})
	if len(got) != 3 {
		t.Fatalf("notes = %v, want 3", got)
	}
	for i, want := range []string{"only be changed from the Mac", "MEDIA_DIR, TMDB_API_KEY is set", "Restart the server"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("note %d = %q, want it to contain %q", i, got[i], want)
		}
	}
}

func TestKeyPlaceholder(t *testing.T) {
	if KeyPlaceholder(true) == KeyPlaceholder(false) {
		t.Error("placeholder should tell whether a key is saved")
	}
}
