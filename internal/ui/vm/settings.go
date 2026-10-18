package vm

import (
	"slices"
	"strings"

	"homemedia/internal/client"
)

// Names of the settings the server can report as overridden by its environment.
const (
	SettingMediaDir = "MEDIA_DIR"
	SettingTMDBKey  = "TMDB_API_KEY"
	SettingTMDBLang = "TMDB_LANGUAGE"
)

// commonLanguages are the TMDB language codes offered in the picker.
var commonLanguages = []string{
	"en-US", "es-ES", "es-MX", "es-AR", "pt-BR", "pt-PT", "fr-FR", "de-DE", "it-IT",
	"nl-NL", "sv-SE", "pl-PL", "ru-RU", "ja-JP", "ko-KR", "zh-CN", "tr-TR",
}

// LanguageOptions is the picker list; a current value outside it is added first
// so the picker can show it.
func LanguageOptions(current string) []string {
	if current == "" || slices.Contains(commonLanguages, current) {
		return slices.Clone(commonLanguages)
	}
	return append([]string{current}, commonLanguages...)
}

// SettingLocked reports whether the server's environment overrides name, so a
// value saved to .env would have no effect.
func SettingLocked(s client.Settings, name string) bool {
	return slices.Contains(s.Overridden, name)
}

// KeyPlaceholder is the hint in the empty TMDB key field.
func KeyPlaceholder(keySet bool) string {
	if keySet {
		return "A key is saved. Paste a new one to replace it."
	}
	return "Paste your TMDB API key (v3 key or v4 read token)"
}

// BuildSettingsUpdate turns the form into the smallest update: only fields that
// differ from the server's settings and are not locked. A non-empty key
// replaces the saved one; removeKey (with an empty key field) deletes it.
// changed is false when there is nothing to save.
func BuildSettingsUpdate(s client.Settings, mediaDir, language, key string, removeKey bool) (u client.SettingsUpdate, changed bool) {
	mediaDir, language, key = strings.TrimSpace(mediaDir), strings.TrimSpace(language), strings.TrimSpace(key)
	if mediaDir != "" && mediaDir != s.MediaDir && !SettingLocked(s, SettingMediaDir) {
		u.MediaDir, changed = &mediaDir, true
	}
	if language != "" && language != s.TMDBLanguage && !SettingLocked(s, SettingTMDBLang) {
		u.TMDBLanguage, changed = &language, true
	}
	if !SettingLocked(s, SettingTMDBKey) {
		switch {
		case key != "":
			u.TMDBKey, changed = &key, true
		case removeKey && s.TMDBKeySet:
			empty := ""
			u.TMDBKey, changed = &empty, true
		}
	}
	return u, changed
}

// SettingsNotes are the lines shown under the form: why it is read-only, which
// fields the environment pins, and whether a restart is pending.
func SettingsNotes(s client.Settings) []string {
	var notes []string
	if !s.Editable {
		notes = append(notes, "Settings can only be changed from the Mac that runs the server.")
	}
	if len(s.Overridden) > 0 {
		notes = append(notes, strings.Join(s.Overridden, ", ")+
			" is set in the server's environment, which wins over .env. Unset it and restart the server to change it here.")
	}
	if s.RestartRequired {
		notes = append(notes, "The new media folder is saved. Restart the server to use it (quit and reopen this app if it started the server).")
	}
	return notes
}
