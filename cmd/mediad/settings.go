package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"homemedia/internal/api"
	"homemedia/internal/config"
	"homemedia/internal/metadata"
)

const (
	envMediaDir = "MEDIA_DIR"
	envTMDBKey  = "TMDB_API_KEY"
	envTMDBLang = "TMDB_LANGUAGE"

	defaultTMDBLanguage = "en-US"
	maxKeyLen           = 512
	checkTimeout        = 10 * time.Second
)

// languageRE is a TMDB language tag such as en, en-US or es-419.
var languageRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,4})?$`)

// settingsStore implements api.SettingsStore on top of the .env file. The TMDB
// key and language take effect at once; the media folder and port are read at
// startup, so a new folder is saved and flagged as waiting for a restart.
type settingsStore struct {
	envPath  string
	port     int
	mediaDir string // the folder this process scans
	meta     *metadata.Client
	check    func(ctx context.Context, key string) error
	rescan   func(refresh bool)
	lookup   func(string) (string, bool) // os.LookupEnv; real env vars beat .env

	mu      sync.Mutex
	pending string // saved MEDIA_DIR that differs from mediaDir
}

var _ api.SettingsStore = (*settingsStore)(nil)

func (s *settingsStore) Settings() api.SettingsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view()
}

func (s *settingsStore) view() api.SettingsView {
	key, lang := s.meta.Credentials()
	if lang == "" {
		lang = defaultTMDBLanguage
	}
	over := []string{}
	for _, name := range []string{envMediaDir, envTMDBKey, envTMDBLang} {
		if _, ok := s.lookup(name); ok {
			over = append(over, name)
		}
	}
	return api.SettingsView{
		MediaDir:        s.mediaDir,
		TMDBLanguage:    lang,
		TMDBKeySet:      key != "",
		Port:            s.port,
		Overridden:      over,
		RestartRequired: s.pending != "" && s.pending != s.mediaDir,
	}
}

func invalid(format string, args ...any) error {
	return &api.InvalidSettingError{Msg: fmt.Sprintf(format, args...)}
}

func (s *settingsStore) Update(ctx context.Context, u api.SettingsUpdate) (api.SettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	curKey, curLang := s.meta.Credentials()
	if curLang == "" {
		curLang = defaultTMDBLanguage
	}
	over := map[string]bool{}
	for _, name := range s.view().Overridden {
		over[name] = true
	}
	refuse := func(name string) error {
		return invalid("%s is set in mediad's environment, which overrides .env. Unset it and restart mediad first.", name)
	}

	updates := map[string]string{}
	var newDir, newKey, newLang string
	var keyChanged, langChanged, dirChanged bool

	if u.MediaDir != nil {
		if over[envMediaDir] {
			return s.view(), refuse(envMediaDir)
		}
		dir, err := config.ResolveDir(*u.MediaDir)
		if err != nil {
			return s.view(), invalid("media folder: %v", err)
		}
		if saved := s.savedDir(); dir != saved {
			newDir, dirChanged = dir, true
			updates[envMediaDir] = dir
		}
	}
	if u.TMDBLanguage != nil {
		if over[envTMDBLang] {
			return s.view(), refuse(envTMDBLang)
		}
		newLang = strings.TrimSpace(*u.TMDBLanguage)
		if newLang == "" {
			newLang = defaultTMDBLanguage
		}
		if !languageRE.MatchString(newLang) {
			return s.view(), invalid("language %q is not a TMDB language code like en-US", newLang)
		}
		if newLang != curLang {
			langChanged = true
			updates[envTMDBLang] = newLang
		}
	}
	if u.TMDBKey != nil {
		if over[envTMDBKey] {
			return s.view(), refuse(envTMDBKey)
		}
		newKey = strings.TrimSpace(*u.TMDBKey)
		if len(newKey) > maxKeyLen || strings.ContainsAny(newKey, " \t\r\n\x00\"'") {
			return s.view(), invalid("that does not look like a TMDB key")
		}
		if newKey != curKey {
			if newKey != "" {
				cctx, cancel := context.WithTimeout(ctx, checkTimeout)
				err := s.check(cctx, newKey)
				cancel()
				if err != nil {
					return s.view(), invalid("TMDB did not accept that key: %v", err)
				}
			}
			keyChanged = true
			updates[envTMDBKey] = newKey
		}
	}
	if len(updates) == 0 {
		return s.view(), nil
	}

	if err := config.UpdateEnvFile(s.envPath, updates); err != nil {
		slog.Error("settings not saved", "err", err)
		return s.view(), err
	}

	if keyChanged || langChanged {
		if !keyChanged {
			newKey = curKey
		}
		if !langChanged {
			newLang = curLang
		}
		s.meta.SetCredentials(newKey, newLang)
	}
	if dirChanged {
		s.pending = newDir
	}
	var changed []string
	for _, name := range []string{envMediaDir, envTMDBKey, envTMDBLang} {
		if _, ok := updates[name]; ok {
			changed = append(changed, name)
		}
	}
	slog.Info("settings saved", "changed", strings.Join(changed, ","))
	switch {
	case keyChanged && newKey == "":
		slog.Info("TMDB key removed")
	case keyChanged:
		slog.Info("TMDB key updated; fetching artwork")
	}
	if (keyChanged && newKey != "") || (langChanged && newKey != "") {
		s.rescan(langChanged) // a new language needs the existing titles fetched again
	}
	return s.view(), nil
}

// savedDir is the folder that the next start will use: a pending change, else
// the running one.
func (s *settingsStore) savedDir() string {
	if s.pending != "" {
		return s.pending
	}
	return s.mediaDir
}
