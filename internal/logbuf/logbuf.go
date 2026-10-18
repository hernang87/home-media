// Package logbuf keeps the most recent log records in memory so clients can read them.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
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
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	max     int
}

// New returns a buffer holding at most max entries.
func New(max int) *Buffer { return &Buffer{max: max, next: 1} }

func (b *Buffer) add(t time.Time, level, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, Entry{Seq: b.next, Time: t, Level: level, Msg: msg})
	b.next++
	if len(b.entries) > b.max {
		b.entries = append([]Entry(nil), b.entries[len(b.entries)-b.max:]...)
	}
}

// After returns the entries with Seq greater than seq, oldest first.
func (b *Buffer) After(seq int) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := 0
	for i < len(b.entries) && b.entries[i].Seq <= seq {
		i++
	}
	return append([]Entry(nil), b.entries[i:]...)
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
	attrs []slog.Attr
	group string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	add := func(a slog.Attr) {
		fmt.Fprintf(&sb, " %s%s=%v", h.group, a.Key, a.Value)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	h.b.add(r.Time, r.Level.String(), sb.String())
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	c.next = h.next.WithAttrs(as)
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	c := *h
	c.group = h.group + name + "."
	c.next = h.next.WithGroup(name)
	return &c
}
