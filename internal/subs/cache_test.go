package subs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"homemedia/internal/model"
)

func TestCacheLoad(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	ass := filepath.Join(dir, "movie.ass")
	srt := filepath.Join(dir, "movie.srt")
	for _, f := range []string{video, ass, srt} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	embedded := model.SubTrack{ID: "i0", Source: "embedded"}
	external := model.SubTrack{ID: "e0", Source: "external", File: ass}
	plain := model.SubTrack{ID: "e1", Source: "external", File: srt}

	tests := []struct {
		name      string
		dir       string
		sub       model.SubTrack
		loads     int
		wantCalls int32
	}{
		{name: "embedded is converted once", dir: "cache", sub: embedded, loads: 3, wantCalls: 1},
		{name: "external ass is converted once", dir: "cache", sub: external, loads: 3, wantCalls: 1},
		{name: "srt is never cached", dir: "cache", sub: plain, loads: 3, wantCalls: 3},
		{name: "no dir converts every time", dir: "", sub: embedded, loads: 2, wantCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c := &Cache{convert: func(string, model.SubTrack) (string, error) {
				calls.Add(1)
				return "WEBVTT\n\nhello", nil
			}}
			if tt.dir != "" {
				c.Dir = filepath.Join(t.TempDir(), tt.dir) // created on first write
			}
			for i := 0; i < tt.loads; i++ {
				got, err := c.Load("item", video, tt.sub)
				if err != nil || got != "WEBVTT\n\nhello" {
					t.Fatalf("Load = %q, %v", got, err)
				}
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("conversions = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestCacheInvalidatesWhenSourceChanges(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	c := &Cache{Dir: filepath.Join(dir, "cache"), convert: func(string, model.SubTrack) (string, error) {
		calls.Add(1)
		return "WEBVTT", nil
	}}
	sub := model.SubTrack{ID: "i0", Source: "embedded"}
	if _, err := c.Load("item", video, sub); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(video, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load("item", video, sub); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("conversions = %d, want 2", got)
	}
	files, _ := filepath.Glob(filepath.Join(c.Dir, "*"))
	if len(files) != 1 {
		t.Errorf("cache holds %v, want only the newest copy", files)
	}
}

func TestCacheErrorsAreNotCached(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fail := true
	c := &Cache{Dir: filepath.Join(dir, "cache"), convert: func(string, model.SubTrack) (string, error) {
		if fail {
			return "", errors.New("boom")
		}
		return "WEBVTT", nil
	}}
	sub := model.SubTrack{ID: "i0", Source: "embedded"}
	if _, err := c.Load("item", video, sub); err == nil {
		t.Fatal("want an error")
	}
	fail = false
	if got, err := c.Load("item", video, sub); err != nil || got != "WEBVTT" {
		t.Fatalf("Load after failure = %q, %v", got, err)
	}
	if _, err := c.Load("item", filepath.Join(dir, "gone.mkv"), sub); err == nil {
		t.Error("want an error for a missing source file")
	}
}

func TestCacheSharesConcurrentConversions(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	c := &Cache{Dir: filepath.Join(dir, "cache"), convert: func(string, model.SubTrack) (string, error) {
		calls.Add(1)
		<-release
		return "WEBVTT", nil
	}}
	sub := model.SubTrack{ID: "i0", Source: "embedded"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := c.Load("item", video, sub); err != nil || got != "WEBVTT" {
				t.Errorf("Load = %q, %v", got, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // let the callers pile up behind the first
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("conversions = %d, want 1", got)
	}
}
