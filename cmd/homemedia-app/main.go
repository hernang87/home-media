// Command homemedia-app is the macOS desktop client for mediad.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/daemon"
	"homemedia/internal/discovery"
	"homemedia/internal/ui"
)

const (
	prefServer = "server"
	localBase  = "http://127.0.0.1:8200"
)

// owned is the mediad this app started, if any. Quitting the app stops it.
var (
	ownedMu sync.Mutex
	owned   *daemon.Proc
)

func stopOwned() {
	ownedMu.Lock()
	p := owned
	ownedMu.Unlock()
	if p != nil {
		p.Stop()
	}
}

//go:embed icon.png
var iconPNG []byte

func main() {
	a := app.NewWithID("com.homemedia.app")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))
	w := a.NewWindow("HomeMedia")
	w.Resize(fyne.NewSize(1000, 720))

	status := widget.NewLabel("Looking for your media server…")
	w.SetContent(container.NewCenter(container.NewVBox(widget.NewProgressBarInfinite(), status)))

	// Cmd-Q and the window close button end up in OnStopped; Ctrl-C and SIGTERM
	// bypass it. A hard kill is covered by mediad watching its parent.
	a.Lifecycle().SetOnStopped(stopOwned)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		stopOwned()
		os.Exit(0)
	}()

	go connect(a, w, status)
	w.ShowAndRun()
}

// connect tries, in order: $HOMEMEDIA_SERVER, the last used server, mDNS discovery,
// then asks for an address.
func connect(a fyne.App, w fyne.Window, status *widget.Label) {
	var candidates []string
	if v := os.Getenv("HOMEMEDIA_SERVER"); v != "" {
		candidates = append(candidates, normalize(v))
	}
	if v := a.Preferences().String(prefServer); v != "" {
		candidates = append(candidates, v)
	}
	for _, base := range candidates {
		if reachable(base) {
			fyne.Do(func() { start(a, w, base) })
			return
		}
	}

	if base, ok := startLocal(status); ok {
		fyne.Do(func() { start(a, w, base) })
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if servers, err := discovery.Browse(ctx, 4); err == nil {
		for _, s := range servers {
			if reachable(s.URL) {
				fyne.Do(func() { start(a, w, s.URL) })
				return
			}
		}
	}
	fyne.Do(func() {
		status.SetText("No server found on this network.")
		askAddress(a, w, status)
	})
}

// startLocal uses a mediad already answering on this machine, or starts one
// that this app then owns and stops on quit.
func startLocal(status *widget.Label) (string, bool) {
	if reachable(localBase) {
		return localBase, true // someone else runs it (launchd, a terminal); not ours to stop
	}
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	cfg, err := daemon.Find(exe)
	if err != nil {
		slog.Info("not starting a local server", "reason", err)
		return "", false
	}
	fyne.Do(func() { status.SetText("Starting the media server…") })
	p, err := daemon.Start(cfg)
	if err != nil {
		slog.Warn("could not start mediad", "err", err)
		return "", false
	}
	ownedMu.Lock()
	owned = p
	ownedMu.Unlock()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-p.Done():
			slog.Warn("mediad exited right after starting", "log", cfg.LogPath)
			return "", false
		case <-deadline:
			p.Stop()
			return "", false
		case <-time.After(500 * time.Millisecond):
			if reachable(localBase) {
				return localBase, true
			}
		}
	}
}

func reachable(base string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.New(base).Info(ctx)
	return err == nil
}

func normalize(v string) string {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	return strings.TrimRight(v, "/")
}

func askAddress(a fyne.App, w fyne.Window, status *widget.Label) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("192.168.1.20:8200")
	form := dialog.NewForm("Server address", "Connect", "Quit", []*widget.FormItem{widget.NewFormItem("Host:port", entry)}, func(ok bool) {
		if !ok {
			a.Quit()
			return
		}
		base := normalize(entry.Text)
		go func() {
			if reachable(base) {
				fyne.Do(func() { start(a, w, base) })
				return
			}
			fyne.Do(func() {
				status.SetText(fmt.Sprintf("Cannot reach %s.", base))
				askAddress(a, w, status)
			})
		}()
	}, w)
	form.Show()
}

func start(a fyne.App, w fyne.Window, base string) {
	a.Preferences().SetString(prefServer, base)
	ui.NewApp(a, w, client.New(base))
}
