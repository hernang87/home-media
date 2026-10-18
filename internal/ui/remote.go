package ui

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/model"
	"homemedia/internal/ui/vm"
)

// remoteScreen is the Remote tab: transport controls for the TV chosen at the top.
// Its widgets are touched only on the UI thread.
type remoteScreen struct {
	a    *App
	st   client.CastStatus // last poll; the buttons work from it
	busy atomic.Bool       // a poll is in flight; the next tick waits for it

	updating bool     // set while the code changes widgets; callbacks then return early
	dragging bool     // the user is moving the seek slider; polls leave it alone
	volDrag  bool     // the user is moving the volume slider
	itemID   string   // item whose subtitle and audio menus are built
	subIDs   []string // subtitle ids, parallel to the sub select's options

	pick      *widget.Label
	body      *fyne.Container
	title     *widget.Label
	state     *widget.Label
	errMsg    *widget.Label
	elapsed   *widget.Label
	total     *widget.Label
	seek      *widget.Slider
	playPause *widget.Button
	vol       *widget.Slider
	mute      *widget.Check
	sub       *widget.Select
	audio     *widget.Select
	subRow    *fyne.Container
	audioRow  *fyne.Container
}

// NewRemote is the Remote tab. A single background ticker re-checks the chosen TV
// once a second and polls it while this tab is on screen.
func NewRemote(a *App) fyne.CanvasObject {
	r := &remoteScreen{a: a}
	r.build()
	go func() {
		for range time.Tick(time.Second) {
			fyne.Do(r.tick)
		}
	}()
	return container.NewPadded(container.NewVBox(r.pick, r.body))
}

func (r *remoteScreen) build() {
	r.pick = widget.NewLabelWithStyle("Pick a TV in the Cast to menu", fyne.TextAlignCenter, fyne.TextStyle{})
	r.title = widget.NewLabelWithStyle("Nothing playing", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	r.state = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{})
	r.errMsg = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	r.errMsg.Hide()

	// Seek: a drag keeps polls off the slider until the user lets go.
	r.elapsed = widget.NewLabel("0:00")
	r.total = widget.NewLabel("0:00")
	r.seek = widget.NewSlider(0, 1)
	r.seek.OnChanged = func(float64) {
		if !r.updating {
			r.dragging = true
		}
	}
	r.seek.OnChangeEnded = func(v float64) {
		if r.updating {
			return
		}
		r.dragging = false
		r.control("seek", v)
	}
	timeRow := container.NewBorder(nil, nil, r.elapsed, r.total, r.seek)

	back := widget.NewButton("-10s", func() { r.seekTo(r.st.Position - 10) })
	fwd := widget.NewButton("+10s", func() { r.seekTo(r.st.Position + 10) })
	r.playPause = widget.NewButton("Play", func() {
		if r.st.State == "PLAYING" {
			r.control("pause", nil)
		} else {
			r.control("resume", nil)
		}
	})
	stop := widget.NewButton("Stop", func() { r.control("stop", nil) })
	buttons := container.NewCenter(container.NewHBox(back, r.playPause, stop, fwd))

	// Volume: same drag rule as seek, so the slider does not fight the user.
	r.vol = widget.NewSlider(0, 1)
	r.vol.Step = 0.05
	r.vol.OnChanged = func(float64) {
		if !r.updating {
			r.volDrag = true
		}
	}
	r.vol.OnChangeEnded = func(v float64) {
		if r.updating {
			return
		}
		r.volDrag = false
		r.control("volume", v)
	}
	r.mute = widget.NewCheck("Mute", func(on bool) {
		if !r.updating {
			r.control("mute", on)
		}
	})
	volRow := container.NewBorder(nil, nil, widget.NewLabel("Volume"), r.mute, r.vol)

	// Subtitle and audio menus; their options are rebuilt when the item changes.
	r.sub = widget.NewSelect(nil, func(string) {
		if r.updating {
			return
		}
		if i := r.sub.SelectedIndex(); i >= 0 && i < len(r.subIDs) {
			r.control("sub", r.subIDs[i])
		}
	})
	r.audio = widget.NewSelect(nil, func(string) {
		if r.updating {
			return
		}
		if i := r.audio.SelectedIndex(); i >= 0 {
			r.control("audio", i)
		}
	})
	r.subRow = container.NewBorder(nil, nil, widget.NewLabel("Subtitles"), nil, r.sub)
	r.audioRow = container.NewBorder(nil, nil, widget.NewLabel("Audio"), nil, r.audio)
	tracks := container.NewGridWithColumns(2, r.subRow, r.audioRow)

	r.body = container.NewVBox(r.title, r.state, r.errMsg, timeRow, buttons, volRow, tracks)
	r.body.Hide()
}

// tick runs every second on the UI thread: it shows either the picker message or
// the controls, then starts a poll if this tab is on screen and a TV is chosen.
func (r *remoteScreen) tick() {
	dev := r.a.DeviceID()
	if (dev != "") != r.body.Visible() {
		if dev != "" {
			r.pick.Hide()
			r.body.Show()
		} else {
			r.body.Hide()
			r.pick.Show()
		}
	}
	if !r.a.RemoteVisible() || dev == "" || r.busy.Load() {
		return
	}
	r.busy.Store(true)
	go func() {
		defer r.busy.Store(false)
		r.poll(dev)
	}()
}

// poll asks the TV for its status and applies the answer on the UI thread.
func (r *remoteScreen) poll(dev string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := r.a.Client.Status(ctx, dev)
	fyne.Do(func() {
		if dev != r.a.DeviceID() {
			return // the user chose another TV meanwhile
		}
		if err != nil {
			r.state.SetText("TV not reachable")
			return
		}
		r.apply(st)
	})
}

// apply shows a status on the widgets. UI thread only.
func (r *remoteScreen) apply(st client.CastStatus) {
	r.updating = true
	defer func() { r.updating = false }()
	r.st = st

	it, ok := r.a.Item(st.ItemID) // zero value when nothing is playing
	if st.ItemID != r.itemID {
		r.setMenus(it)
		if ok || st.ItemID == "" { // an unknown item is retried on the next poll
			r.itemID = st.ItemID
		}
	}

	title := "Nothing playing"
	if st.ItemID != "" {
		title = "Unknown title"
		if ok {
			title = it.Title
		}
	}
	r.title.SetText(title)
	r.state.SetText(st.State)
	if st.Error != "" {
		r.errMsg.SetText(st.Error)
		r.errMsg.Show()
	} else {
		r.errMsg.Hide()
	}
	r.elapsed.SetText(vm.Clock(st.Position))
	r.total.SetText(vm.Clock(st.Duration))

	// The slider needs Max above Min, so an unknown duration becomes 1 s.
	r.seek.Max = max(st.Duration, 1)
	if !r.dragging {
		r.seek.SetValue(st.Position)
	}
	r.seek.Refresh()

	if !r.volDrag {
		r.vol.SetValue(st.Volume)
	}
	r.mute.SetChecked(st.Muted)

	if st.State == "PLAYING" {
		r.playPause.SetText("Pause")
	} else {
		r.playPause.SetText("Play")
	}

	for i, id := range r.subIDs {
		if id == st.Sub {
			r.sub.SetSelectedIndex(i)
			break
		}
	}
	r.audio.SetSelectedIndex(st.Audio)
}

// setMenus fills the subtitle and audio menus for it. UI thread only.
func (r *remoteScreen) setMenus(it model.PublicItem) {
	r.subIDs = nil
	var subLabels []string
	for _, c := range vm.SubChoices(it.Subs) {
		subLabels = append(subLabels, c.Label)
		r.subIDs = append(r.subIDs, c.ID)
	}
	r.sub.SetOptions(subLabels)
	r.sub.ClearSelected()

	var audioLabels []string
	for i, t := range it.Audio {
		audioLabels = append(audioLabels, vm.AudioLabel(i, t))
	}
	r.audio.SetOptions(audioLabels)
	r.audio.ClearSelected()

	// Subtitles show for any item; the audio menu only when there is a choice.
	if it.ID != "" {
		r.subRow.Show()
	} else {
		r.subRow.Hide()
	}
	if len(it.Audio) > 1 {
		r.audioRow.Show()
	} else {
		r.audioRow.Hide()
	}
}

// seekTo seeks to pos seconds, kept within the item's length.
func (r *remoteScreen) seekTo(pos float64) {
	pos = max(pos, 0)
	pos = min(pos, r.st.Duration)
	r.control("seek", pos)
}

// control sends one transport command to the chosen TV, then polls at once.
func (r *remoteScreen) control(action string, value any) {
	dev := r.a.DeviceID()
	if dev == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := r.a.Client.Control(ctx, dev, action, value); err != nil {
			r.a.ShowError(fmt.Errorf("could not %s: %w", action, err))
			return
		}
		r.poll(dev)
	}()
}
