package library

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// cacheKey is the item ID or show key a cache file belongs to: the part of its
// name before the first dot. Art is "<key>.<kind>.jpg", converted subtitles
// "<itemID>.<subID>.<version>.vtt" and keyframe indexes "<itemID>.json".
func cacheKey(name string) string {
	key, _, _ := strings.Cut(name, ".")
	return key
}

// liveKeysLocked is every key a cache file may use: item IDs and "show-<key>".
func (l *Library) liveKeysLocked() map[string]bool {
	live := make(map[string]bool, len(l.items)+len(l.shows))
	for id := range l.items {
		live[id] = true
	}
	for key := range l.shows {
		live["show-"+key] = true
	}
	return live
}

// pruneCaches deletes the art, subtitle and keyframe files of items and shows
// that are no longer in the library (deleted or renamed files). Files that
// change in place already replace their own cache entries.
func (l *Library) pruneCaches() {
	l.mu.RLock()
	live := l.liveKeysLocked()
	l.mu.RUnlock()
	removed := 0
	for _, dir := range []string{l.cfg.ArtDir, l.cfg.SubDir, l.cfg.KeyDir} {
		removed += pruneDir(dir, live)
	}
	if removed > 0 {
		slog.Info("pruned cache files of removed items", "files", removed)
	}
}

// pruneDir deletes the regular files in dir whose cacheKey is not in live and
// returns how many it deleted. A missing dir is not an error.
func pruneDir(dir string, live map[string]bool) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("cannot read cache dir", "dir", dir, "err", err)
		}
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || live[cacheKey(e.Name())] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			slog.Warn("cannot remove cache file", "file", e.Name(), "err", err)
			continue
		}
		n++
	}
	return n
}
