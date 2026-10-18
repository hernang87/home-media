// Package ui is the Fyne desktop client. Rules for every file in this package:
// network calls run in goroutines; widgets are touched only on the UI thread,
// so anything a goroutine wants to show goes through fyne.Do.
package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/model"
	"homemedia/internal/ui/vm"
)

const prefDevice = "device"

// App is the state shared by all screens.
type App struct {
	Fyne   fyne.App
	Win    fyne.Window
	Client *client.Client
	Art    *ArtCache

	mu      sync.RWMutex
	snap    model.Snapshot
	devices []client.Device

	tabs       *container.AppTabs
	libraryTab *container.TabItem
	remoteTab  *container.TabItem
	logsTab    *container.TabItem
	phoneTab   *container.TabItem
	libStack   []fyne.CanvasObject // navigation stack of the Library tab; [0] is the grid
	libHolder  *fyne.Container

	deviceSel *widget.Select
	onReload  []func() // called on the UI thread after the library changes
}

// NewApp builds the main window content for a connected client.
func NewApp(a fyne.App, w fyne.Window, c *client.Client) *App {
	app := &App{Fyne: a, Win: w, Client: c}
	app.Art = NewArtCache(300, c.FetchArt)

	app.libHolder = container.NewStack()
	app.libraryTab = container.NewTabItem("Library", app.libHolder)
	app.remoteTab = container.NewTabItem("Remote", NewRemote(app))
	app.logsTab = container.NewTabItem("Logs", NewLogs(app))
	phone := NewPhone(app)
	app.phoneTab = container.NewTabItem("Phone", phone.Object())
	app.tabs = container.NewAppTabs(app.libraryTab, app.remoteTab, app.phoneTab, app.logsTab)
	app.tabs.OnSelected = func(t *container.TabItem) {
		if t == app.phoneTab {
			phone.load()
		}
	}

	app.deviceSel = widget.NewSelect(nil, func(name string) {
		if d, ok := app.deviceByName(name); ok {
			a.Preferences().SetString(prefDevice, d.ID)
		}
	})
	app.deviceSel.PlaceHolder = "Choose a TV"
	refresh := widget.NewButton("Refresh", func() { app.Reload(); app.ReloadDevices() })
	top := container.NewBorder(nil, nil, widget.NewLabel("Cast to"), refresh, app.deviceSel)

	app.Push(NewLibrary(app))
	w.SetContent(container.NewBorder(top, nil, nil, nil, app.tabs))

	app.Reload()
	app.ReloadDevices()
	go app.tick()
	return app
}

func (a *App) tick() {
	for range time.Tick(20 * time.Second) {
		a.Reload()
		a.ReloadDevices()
	}
}

// OnReload registers fn to run on the UI thread whenever the library is reloaded.
func (a *App) OnReload(fn func()) { a.onReload = append(a.onReload, fn) }

// Snapshot returns the last loaded library.
func (a *App) Snapshot() model.Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snap
}

// Item finds an item by ID in the last loaded library.
func (a *App) Item(id string) (model.PublicItem, bool) {
	for _, it := range a.Snapshot().Items {
		if it.ID == id {
			return it, true
		}
	}
	return model.PublicItem{}, false
}

// Show finds a show by key in the last loaded library.
func (a *App) Show(key string) (model.PublicShow, bool) {
	for _, s := range a.Snapshot().Shows {
		if s.Key == key {
			return s, true
		}
	}
	return model.PublicShow{}, false
}

// Reload fetches the library in the background and notifies listeners.
func (a *App) Reload() {
	go func() { _ = a.ReloadSync() }()
}

// ReloadSync is Reload that waits: it returns once the new snapshot is stored and
// the reload hooks are queued on the UI thread. Call it from a goroutine.
func (a *App) ReloadSync() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snap, err := a.Client.Library(ctx)
	if err != nil {
		return err // the next tick retries; a flaky LAN should not pop dialogs
	}
	a.mu.Lock()
	a.snap = snap
	a.mu.Unlock()
	fyne.Do(func() {
		for _, fn := range a.onReload {
			fn()
		}
	})
	return nil
}

// ReloadDevices refreshes the TV list and keeps the remembered choice selected.
func (a *App) ReloadDevices() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		devs, err := a.Client.Devices(ctx)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.devices = devs
		a.mu.Unlock()
		fyne.Do(func() {
			names := make([]string, len(devs))
			for i, d := range devs {
				names[i] = d.Name
			}
			keep := a.deviceSel.Selected
			a.deviceSel.Options = names
			if d, ok := a.deviceByName(keep); ok {
				keep = d.Name
			} else if id := a.Fyne.Preferences().String(prefDevice); id != "" {
				keep = ""
				for _, d := range devs {
					if d.ID == id {
						keep = d.Name
					}
				}
			} else {
				keep = ""
			}
			if keep == "" && len(devs) == 1 {
				keep = devs[0].Name
			}
			a.deviceSel.Selected = keep
			a.deviceSel.Refresh()
		})
	}()
}

func (a *App) deviceByName(name string) (client.Device, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, d := range a.devices {
		if d.Name == name {
			return d, true
		}
	}
	return client.Device{}, false
}

// DeviceID is the ID of the TV chosen at the top, or "" when none is chosen.
func (a *App) DeviceID() string {
	if d, ok := a.deviceByName(a.deviceSel.Selected); ok {
		return d.ID
	}
	return ""
}

// Push shows obj in the Library tab on top of the current screen.
func (a *App) Push(obj fyne.CanvasObject) {
	a.libStack = append(a.libStack, obj)
	a.libHolder.Objects = []fyne.CanvasObject{obj}
	a.libHolder.Refresh()
	a.tabs.Select(a.libraryTab)
}

// Pop goes back one screen in the Library tab.
func (a *App) Pop() {
	if len(a.libStack) <= 1 {
		return
	}
	a.libStack = a.libStack[:len(a.libStack)-1]
	a.libHolder.Objects = []fyne.CanvasObject{a.libStack[len(a.libStack)-1]}
	a.libHolder.Refresh()
}

// ShowRemote switches to the Remote tab.
func (a *App) ShowRemote() { a.tabs.Select(a.remoteTab) }

// ShowError reports err in a dialog, from any goroutine.
func (a *App) ShowError(err error) {
	fyne.Do(func() { dialog.ShowError(err, a.Win) })
}

// PlayItem casts an item to the chosen TV. A partly watched item first asks
// "Continue from XX:XX" or "Restart". audio is an audio track index; sub is a
// subtitle id or "" for none.
func (a *App) PlayItem(it model.PublicItem, audio int, sub string) {
	a.playWith(it, audio, sub, "")
}

func (a *App) playWith(it model.PublicItem, audio int, sub, resume string) {
	dev := a.DeviceID()
	if dev == "" {
		dialog.ShowInformation("Choose a TV", "Pick a TV in the \"Cast to\" menu first.", a.Win)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		defer cancel()
		_, err := a.Client.Play(ctx, dev, client.PlayRequest{ID: it.ID, Audio: audio, Sub: sub, Resume: resume})
		var re *client.ResumeError
		switch {
		case errors.As(err, &re):
			fyne.Do(func() { a.askResume(it, audio, sub, re.Position) })
		case err != nil:
			a.ShowError(fmt.Errorf("could not play %q: %w", it.Title, err))
		default:
			a.Reload() // progress changes as soon as playback starts
			fyne.Do(a.ShowRemote)
		}
	}()
}

func (a *App) askResume(it model.PublicItem, audio int, sub string, pos float64) {
	var d dialog.Dialog
	choose := func(resume string) func() {
		return func() {
			d.Hide()
			a.playWith(it, audio, sub, resume)
		}
	}
	body := container.NewVBox(
		widget.NewLabel("You already watched part of this."),
		widget.NewButton(vm.ResumeLabel(pos), choose("continue")),
		widget.NewButton("Restart from the beginning", choose("restart")),
	)
	d = dialog.NewCustom(it.Title, "Cancel", body, a.Win)
	d.Show()
}

// RemoteVisible reports whether the Remote tab is the one on screen. UI thread only.
func (a *App) RemoteVisible() bool { return a.tabs.Selected() == a.remoteTab }
