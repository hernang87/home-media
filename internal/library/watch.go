package library

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce is how long the media folder must be quiet before a rescan,
// so a file still being copied is not scanned half-written. Tests shorten it.
var watchDebounce = 10 * time.Second

// Watch rescans (debounced by watchDebounce) whenever something under the media folder
// changes, until ctx is cancelled. fsnotify is not recursive, so every
// directory is added and new ones are added as they appear.
func (l *Library) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && p != root {
					return filepath.SkipDir
				}
				if err := w.Add(p); err != nil {
					slog.Warn("watch failed", "dir", p, "err", err)
				}
			}
			return nil
		})
	}
	addTree(l.cfg.MediaDir)

	rescan := newDebouncer(watchDebounce, func() {
		if err := l.Scan(ctx, false); err != nil && ctx.Err() == nil {
			slog.Error("scan failed", "err", err)
		}
	})
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-w.Errors:
			slog.Warn("watcher error", "err", err)
		case ev := <-w.Events:
			if strings.HasPrefix(filepath.Base(ev.Name), ".") {
				continue
			}
			if ev.Has(fsnotify.Create) {
				addTree(ev.Name) // no-op for plain files
			}
			rescan.Trigger()
		}
	}
}
