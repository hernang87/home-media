package logbuf

import (
	"io"
	"log/slog"
	"strconv"
	"testing"
)

func TestBufferKeepsLatestAndPagesBySeq(t *testing.T) {
	b := New(3)
	log := slog.New(b.Handler(slog.NewTextHandler(io.Discard, nil)))
	for i := 1; i <= 5; i++ {
		log.Info("m", "n", i)
	}
	got := b.After(0)
	if len(got) != 3 || got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("After(0) = %+v", got)
	}
	if got[2].Msg != "m n=5" || got[2].Level != "INFO" {
		t.Errorf("entry = %+v", got[2])
	}
	if rest := b.After(4); len(rest) != 1 || rest[0].Seq != 5 {
		t.Errorf("After(4) = %+v", rest)
	}
	if rest := b.After(5); len(rest) != 0 {
		t.Errorf("After(5) = %+v", rest)
	}
}

func TestBufferWraparound(t *testing.T) {
	tests := []struct {
		name  string
		max   int
		logs  int
		after int
		want  []int // Seq values, oldest first
	}{
		{"not full", 3, 2, 0, []int{1, 2}},
		{"exactly full", 3, 3, 0, []int{1, 2, 3}},
		{"wrapped once", 3, 7, 0, []int{5, 6, 7}},
		{"wrapped, after mid", 3, 7, 5, []int{6, 7}},
		{"wrapped, after last", 3, 7, 7, nil},
		{"wrapped, after beyond", 3, 7, 99, nil},
		{"wrapped, negative after", 3, 7, -1, []int{5, 6, 7}},
		{"wrapped, after evicted", 3, 7, 2, []int{5, 6, 7}},
		{"many wraps", 4, 23, 20, []int{21, 22, 23}},
		{"zero max keeps nothing", 0, 5, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.max)
			log := slog.New(b.Handler(slog.NewTextHandler(io.Discard, nil)))
			for i := 1; i <= tt.logs; i++ {
				log.Info("m", "n", i)
			}
			got := b.After(tt.after)
			if len(got) != len(tt.want) {
				t.Fatalf("After(%d) len = %d, want %d: %+v", tt.after, len(got), len(tt.want), got)
			}
			for i, e := range got {
				if e.Seq != tt.want[i] || e.Msg != "m n="+strconv.Itoa(tt.want[i]) {
					t.Errorf("entry %d = %+v, want seq %d", i, e, tt.want[i])
				}
			}
			if b.Last() != tt.logs {
				t.Errorf("Last() = %d, want %d", b.Last(), tt.logs)
			}
		})
	}
}

func TestHandlerGroupPrefixes(t *testing.T) {
	tests := []struct {
		name string
		log  func(*slog.Logger) *slog.Logger
		want string
	}{
		{"attr before group", func(l *slog.Logger) *slog.Logger {
			return l.With("a", 1).WithGroup("g").With("b", 2)
		}, "m a=1 g.b=2 g.c=3"},
		{"empty group is a no-op", func(l *slog.Logger) *slog.Logger {
			return l.WithGroup("").With("a", 1)
		}, "m a=1 c=3"},
		{"empty attrs are a no-op", func(l *slog.Logger) *slog.Logger {
			return l.With().With("a", 1)
		}, "m a=1 c=3"},
		{"nested groups", func(l *slog.Logger) *slog.Logger {
			return l.WithGroup("x").With("a", 1).WithGroup("y")
		}, "m x.a=1 x.y.c=3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(5)
			log := tt.log(slog.New(b.Handler(slog.NewTextHandler(io.Discard, nil))))
			log.Info("m", "c", 3)
			if e := b.After(0)[0]; e.Msg != tt.want {
				t.Errorf("msg = %q, want %q", e.Msg, tt.want)
			}
		})
	}
}

func TestHandlerIncludesWithAttrs(t *testing.T) {
	b := New(5)
	log := slog.New(b.Handler(slog.NewTextHandler(io.Discard, nil))).With("dev", "tv")
	log.Warn("hi")
	if e := b.After(0)[0]; e.Msg != "hi dev=tv" || e.Level != "WARN" {
		t.Errorf("entry = %+v", e)
	}
}
