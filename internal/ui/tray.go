package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"

	"homemedia/internal/ui/vm"
)

// Tray is the menu bar menu: what the chosen TV is playing, Pause/Resume and
// Stop, and Open. Fyne adds Quit. Its items are touched only on the UI thread.
type Tray struct {
	Menu *fyne.Menu

	app    *App
	status *fyne.MenuItem
	toggle *fyne.MenuItem
	stop   *fyne.MenuItem
	shown  vm.Tray // last state put on the items; a refresh happens only on change
	dev    string  // TV the last poll was for
	busy   bool    // a poll is in flight; the next tick waits for it
}

// NewTray builds the menu. open brings the main window back. The status items
// stay idle until Attach.
func NewTray(open func()) *Tray {
	t := &Tray{}
	t.status = fyne.NewMenuItem("Nothing playing", nil)
	t.status.Disabled = true
	t.toggle = fyne.NewMenuItem("Pause", func() { t.control(t.shown.ToggleAction()) })
	t.toggle.Disabled = true
	t.stop = fyne.NewMenuItem("Stop", func() { t.control("stop") })
	t.stop.Disabled = true
	t.Menu = fyne.NewMenu("HomeMedia",
		t.status, t.toggle, t.stop,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Open HomeMedia", open),
	)
	return t
}

// Attach starts polling the TV chosen in app. Call it once, on the UI thread.
func (t *Tray) Attach(app *App) {
	t.app = app
	go func() {
		for range time.Tick(3 * time.Second) {
			fyne.Do(t.tick)
		}
	}()
}

// tick starts a poll of the chosen TV. UI thread only.
func (t *Tray) tick() {
	dev := t.app.DeviceID()
	t.dev = dev
	if dev == "" {
		t.apply(vm.Tray{Label: "Choose a TV in HomeMedia", ToggleText: "Pause"})
		return
	}
	if t.busy {
		return
	}
	t.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := t.app.Client.Status(ctx, dev)
		fyne.Do(func() {
			t.busy = false
			if dev != t.dev {
				return // the user chose another TV meanwhile
			}
			if err != nil {
				t.apply(vm.Tray{Label: "TV not reachable", ToggleText: "Pause"})
				return
			}
			title := ""
			if it, ok := t.app.Item(st.ItemID); ok {
				title = it.Title
			}
			t.apply(vm.TrayStatus(st, title))
		})
	}()
}

// apply puts v on the menu items, refreshing the menu only when something changed.
func (t *Tray) apply(v vm.Tray) {
	if v == t.shown {
		return
	}
	t.shown = v
	t.status.Label = v.Label
	t.toggle.Label = v.ToggleText
	t.toggle.Disabled = !v.Active
	t.stop.Disabled = !v.Active
	t.Menu.Refresh()
}

// control sends a transport command to the chosen TV, then polls at once.
func (t *Tray) control(action string) {
	dev := t.dev
	if dev == "" || t.app == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := t.app.Client.Control(ctx, dev, action, nil); err != nil {
			t.app.ShowError(fmt.Errorf("could not %s: %w", action, err))
			return
		}
		fyne.Do(t.tick)
	}()
}
