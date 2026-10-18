package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"homemedia/internal/daemon"
)

// watchParent calls onGone once the process that started mediad (named by
// daemon.ParentEnv) is gone. Without the variable it does nothing, so launchd
// and terminal runs are unaffected.
func watchParent(ctx context.Context, onGone func()) {
	want, err := strconv.Atoi(os.Getenv(daemon.ParentEnv))
	if err != nil || want <= 1 {
		return
	}
	slog.Info("started by the desktop app; will exit when it quits", "app_pid", want)
	go pollParent(ctx, want, time.Second, os.Getppid, onGone)
}

// pollParent returns when ctx ends, or after calling onGone once getppid stops
// returning want (a child that loses its parent is adopted by launchd, pid 1).
func pollParent(ctx context.Context, want int, every time.Duration, getppid func() int, onGone func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if getppid() != want {
				onGone()
				return
			}
		}
	}
}
