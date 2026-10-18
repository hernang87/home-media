package library

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// VideoExts and SubExts are the file extensions the scanner picks up.
var (
	VideoExts = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".webm": true, ".ts": true, ".wmv": true, ".mpg": true, ".mpeg": true}
	SubExts   = map[string]bool{".srt": true, ".vtt": true, ".ass": true, ".ssa": true}
)

// IDFor is the stable item ID: first 12 hex chars of sha1 over the NFC relative path.
func IDFor(rel string) string {
	sum := sha1.Sum([]byte(norm.NFC.String(rel)))
	return hex.EncodeToString(sum[:])[:12]
}

// Slug lowercases, strips diacritics and collapses non-alphanumerics to "-".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// mapLimit runs fn over list with at most limit concurrent calls.
func mapLimit[T any](list []T, limit int, fn func(T)) {
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, v := range list {
		sem <- struct{}{}
		wg.Add(1)
		go func(v T) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(v)
		}(v)
	}
	wg.Wait()
}

// debouncer calls fn once, delay after the last Trigger.
type debouncer struct {
	mu    sync.Mutex
	timer *time.Timer
	delay time.Duration
	fn    func()
}

func newDebouncer(delay time.Duration, fn func()) *debouncer {
	return &debouncer{delay: delay, fn: fn}
}

func (d *debouncer) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, d.fn)
}

// runFFmpeg runs ffmpeg with a timeout, discarding output.
func runFFmpeg(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "ffmpeg", args...).Run()
}
