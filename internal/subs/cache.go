package subs

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"homemedia/internal/model"
)

// Cache keeps the WebVTT that ffmpeg produced for a subtitle on disk, so an
// embedded track is extracted from its video once instead of on every request.
// The zero value (empty Dir) converts every time. Safe for concurrent use.
type Cache struct {
	Dir string

	mu       sync.Mutex
	inflight map[string]*cacheCall
	// convert is LoadVTT; tests replace it.
	convert func(videoPath string, sub model.SubTrack) (string, error)
}

type cacheCall struct {
	done chan struct{}
	vtt  string
	err  error
}

// needsFFmpeg reports whether LoadVTT runs ffmpeg for sub. The other
// subtitles are converted in Go, which is cheaper than a cache lookup is worth.
func needsFFmpeg(sub model.SubTrack) bool {
	if sub.Source == "embedded" {
		return true
	}
	ext := strings.ToLower(nodeExtname(filepath.Base(sub.File)))
	return ext != ".srt" && ext != ".vtt"
}

// Load returns the WebVTT of sub for the item itemID at videoPath. A cached
// copy is used until the file the subtitle comes from changes (size or mtime).
// Concurrent requests for the same subtitle share one conversion.
func (c *Cache) Load(itemID, videoPath string, sub model.SubTrack) (string, error) {
	convert := c.convert
	if convert == nil {
		convert = LoadVTT
	}
	if c.Dir == "" || !needsFFmpeg(sub) {
		return convert(videoPath, sub)
	}
	source := sub.File
	if sub.Source == "embedded" {
		source = videoPath
	}
	st, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	prefix := fmt.Sprintf("%s.%s.", itemID, sub.ID)
	path := filepath.Join(c.Dir, fmt.Sprintf("%s%x-%x.vtt", prefix, st.ModTime().UnixNano(), st.Size()))

	c.mu.Lock()
	if call := c.inflight[path]; call != nil {
		c.mu.Unlock()
		<-call.done
		return call.vtt, call.err
	}
	call := &cacheCall{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = map[string]*cacheCall{}
	}
	c.inflight[path] = call
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inflight, path)
		c.mu.Unlock()
		close(call.done)
	}()

	if b, err := os.ReadFile(path); err == nil {
		call.vtt = string(b)
		return call.vtt, nil
	}
	call.vtt, call.err = convert(videoPath, sub)
	if call.err != nil {
		return "", call.err
	}
	c.store(path, prefix, call.vtt)
	return call.vtt, nil
}

// store writes vtt to path and removes the copies made from older versions of
// the source file. A failure only costs the next request a conversion.
func (c *Cache) store(path, prefix, vtt string) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		slog.Warn("subtitle cache unavailable", "err", err)
		return
	}
	if old, err := filepath.Glob(filepath.Join(c.Dir, prefix+"*.vtt")); err == nil {
		for _, f := range old {
			_ = os.Remove(f)
		}
	}
	// Write then rename so a reader never sees half a file.
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, []byte(vtt), 0o644)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		slog.Warn("subtitle cache write failed", "file", path, "err", err)
	}
}
