package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollParent(t *testing.T) {
	t.Run("calls onGone when the parent changes", func(t *testing.T) {
		var ppid atomic.Int64
		ppid.Store(500)
		gone := make(chan struct{})
		go pollParent(context.Background(), 500, 5*time.Millisecond, func() int { return int(ppid.Load()) }, func() { close(gone) })
		select {
		case <-gone:
			t.Fatal("onGone fired while the parent was alive")
		case <-time.After(40 * time.Millisecond):
		}
		ppid.Store(1) // adopted by launchd
		select {
		case <-gone:
		case <-time.After(time.Second):
			t.Fatal("onGone never fired")
		}
	})

	t.Run("stops when ctx ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			pollParent(ctx, 500, 5*time.Millisecond, func() int { return 1 }, func() { t.Error("onGone fired after cancel") })
			close(done)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("pollParent did not return")
		}
	})
}
