package logbuf

import (
	"io"
	"log/slog"
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

func TestHandlerIncludesWithAttrs(t *testing.T) {
	b := New(5)
	log := slog.New(b.Handler(slog.NewTextHandler(io.Discard, nil))).With("dev", "tv")
	log.Warn("hi")
	if e := b.After(0)[0]; e.Msg != "hi dev=tv" || e.Level != "WARN" {
		t.Errorf("entry = %+v", e)
	}
}
