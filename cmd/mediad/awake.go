package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
)

// keepAwake stops the Mac from idle-sleeping while mediad runs, so the phone
// page and the TV stay reachable. caffeinate exits by itself when mediad does.
func keepAwake(ctx context.Context) {
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return // not macOS
	}
	cmd := exec.CommandContext(ctx, path, "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		slog.Warn("could not prevent idle sleep", "err", err)
		return
	}
	slog.Info("preventing idle sleep while mediad runs")
	go func() { _ = cmd.Wait() }()
}
