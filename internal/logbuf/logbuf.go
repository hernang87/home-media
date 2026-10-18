// Package logbuf keeps the most recent log records in memory so clients can read them.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Entry is one log record.
type Entry struct {
	Seq   int       `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// Buffer is a fixed-size ring of entries with increasing sequence numbers.
// Sequence numbers are contiguous, so the oldest entry has Seq next-count.
type Buffer struct {
	mu    sync.Mutex
	ring  []Entry // fixed backing array of max entries
	start int     // ring index of the oldest entry
	count int
	next  int
	max   int
}

// New returns a buffer holding at most max entries.
func New(max int) *Buffer {
	b := &Buffer{max: max, next: 1}
	if max > 0 {
		b.ring = make([]Entry, max)
	}
	return b
}

func (b *Buffer) add(t time.Time, level, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := Entry{Seq: b.next, Time: t, Level: level, Msg: msg}
	b.next++
	switch {
	case b.max <= 0:
	case b.count < b.max:
		b.ring[(b.start+b.count)%b.max] = e
		b.count++
	default:
		b.ring[b.start] = e // overwrite the oldest
		b.start = (b.start + 1) % b.max
	}
}

// After returns the entries with Seq greater than seq, oldest first.
func (b *Buffer) After(seq int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	oldest := b.next - b.count
	skip := min(max(seq-oldest+1, 0), b.count)
	out := make([]Entry, b.count-skip)
	for k := range out {
		out[k] = b.ring[(b.start+skip+k)%b.max]
	}
	return out
}

// Last returns the highest sequence number handed out so far, 0 when empty.
func (b *Buffer) Last() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next - 1
}

// Handler returns a slog.Handler that records into b and then passes on to next.
func (b *Buffer) Handler(next slog.Handler) slog.Handler { return &handler{b: b, next: next} }

type handler struct {
	b     *Buffer
	next  slog.Handler
	attrs []slog.Attr // keys already carry the group prefix in force when added
	group string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s%s=%v", h.group, a.Key, a.Value)
		return true
	})
	h.b.add(r.Time, r.Level.String(), sb.String())
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	c := *h
	c.attrs = slices.Clone(h.attrs)
	for _, a := range as {
		a.Key = h.group + a.Key
		c.attrs = append(c.attrs, a)
	}
	c.next = h.next.WithAttrs(as)
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.group = h.group + name + "."
	c.next = h.next.WithGroup(name)
	return &c
}
