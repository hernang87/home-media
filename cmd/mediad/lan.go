package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// watchLAN polls the machine's LAN address and calls set with the new address and base URL
// when it changes (DHCP renewal, Wi-Fi switch), so the Chromecast keeps getting
// URLs it can reach. It returns when ctx ends.
func watchLAN(ctx context.Context, port int, every time.Duration, current string, lookup func() string, set func(lan, base string)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if lan := lookup(); lan != current {
				current = lan
				base := fmt.Sprintf("http://%s:%d", lan, port)
				slog.Info("LAN address changed", "url", base)
				set(lan, base)
			}
		}
	}
}
