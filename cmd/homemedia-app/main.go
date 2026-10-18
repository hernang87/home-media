// Command homemedia-app is the macOS desktop client for mediad.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/discovery"
	"homemedia/internal/ui"
)

const prefServer = "server"

//go:embed icon.png
var iconPNG []byte

func main() {
	a := app.NewWithID("com.homemedia.app")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))
	w := a.NewWindow("HomeMedia")
	w.Resize(fyne.NewSize(1000, 720))

	status := widget.NewLabel("Looking for your media server…")
	w.SetContent(container.NewCenter(container.NewVBox(widget.NewProgressBarInfinite(), status)))

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
