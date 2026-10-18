package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/model"
	"homemedia/internal/ui/vm"
)

const (
	posterW = 150
	posterH = 225
)

// tile is a poster with a title and a watch-progress bar.
type tile struct {
	widget.BaseWidget
	key   string // what the tile currently shows; guards async image delivery
	img   *canvas.Image
	title *widget.Label
	bar   *strip
}

func newTile() *tile {
	t := &tile{img: canvas.NewImageFromResource(nil), title: widget.NewLabel(""), bar: newStrip()}
	t.img.FillMode = canvas.ImageFillCover
	t.img.SetMinSize(fyne.NewSize(posterW, posterH))
	t.title.Truncation = fyne.TextTruncateEllipsis
	t.title.Alignment = fyne.TextAlignCenter
	t.ExtendBaseWidget(t)
	return t
}

func (t *tile) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewBorder(nil, container.NewVBox(t.bar, t.title), nil, nil, t.img))
}

// set shows one entry; the poster arrives later through the art cache.
func (t *tile) set(a *App, key, title string, poster *string, frac float64) {
	t.key = key
	t.title.SetText(title)
	t.img.Resource = theme.FileVideoIcon()
	t.img.Refresh()
	if frac > 0 && frac < 1 {
		t.bar.set(frac)
		t.bar.Show()
	} else {
		t.bar.Hide()
	}
	a.Art.Get(poster, func(res fyne.Resource) {
		if t.key == key {
			t.img.Resource = res
			t.img.Refresh()
		}
	})
}

// strip is a thin watch-progress bar (widget.ProgressBar is too tall for a poster).
type strip struct {
	widget.BaseWidget
	frac float64
}

func newStrip() *strip {
	s := &strip{}
	s.ExtendBaseWidget(s)
	return s
}

func (s *strip) set(frac float64) {
	s.frac = frac
	s.Refresh()
}

func (s *strip) CreateRenderer() fyne.WidgetRenderer {
	return &stripRenderer{s: s, track: canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)), fill: canvas.NewRectangle(theme.Color(theme.ColorNamePrimary))}
}

type stripRenderer struct {
	s           *strip
	track, fill *canvas.Rectangle
}

func (r *stripRenderer) Layout(size fyne.Size) {
	r.track.Resize(size)
	r.fill.Resize(fyne.NewSize(size.Width*float32(r.s.frac), size.Height))
}
func (r *stripRenderer) MinSize() fyne.Size { return fyne.NewSize(posterW, 4) }
func (r *stripRenderer) Refresh() {
	r.Layout(r.s.Size())
	r.track.Refresh()
	r.fill.Refresh()
}
func (r *stripRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.track, r.fill} }
func (r *stripRenderer) Destroy()                     {}

// tappable makes any object clickable without the list's selection machinery.
type tappable struct {
	widget.BaseWidget
	child fyne.CanvasObject
	fn    func()
}

func newTappable(child fyne.CanvasObject, fn func()) *tappable {
	t := &tappable{child: child, fn: fn}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappable) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.child) }
func (t *tappable) Tapped(*fyne.PointEvent)             { t.fn() }

// NewLibrary is the home screen: search, a "continue watching" row and the poster grid.
func NewLibrary(a *App) fyne.CanvasObject {
	var (
		shelves []vm.Shelf
		query   string
	)
	grid := widget.NewGridWrap(
		func() int { return len(shelves) },
		func() fyne.CanvasObject { return newTile() },
		func(i widget.GridWrapItemID, o fyne.CanvasObject) {
			s := shelves[i]
			frac := 0.0
			if it, ok := a.Item(s.Key); ok && !s.IsShow {
				frac = vm.ProgressFraction(it)
			}
			o.(*tile).set(a, s.Key, s.Title, s.Poster, frac)
		},
	)
	grid.OnSelected = func(i widget.GridWrapItemID) {
		s := shelves[i]
		grid.Unselect(i)
		a.Push(NewDetail(a, s.Key, s.IsShow))
	}

	cont := container.NewHBox()
	contHeader := widget.NewLabelWithStyle("Continue watching", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	contScroll := container.NewHScroll(cont)
	contBox := container.NewVBox(contHeader, contScroll)

	rebuild := func() {
		snap := a.Snapshot()
		shelves = vm.Shelves(snap, query)
		grid.Refresh()

		cont.Objects = nil
		for _, it := range vm.ContinueWatching(snap.Items, 12) {
			it := it
			t := newTile()
			title := it.Title
			if it.Show != nil {
				title = it.Show.Title + " " + vm.EpisodeLabel(it)
			}
			t.set(a, it.ID, title, posterFor(snap, it), vm.ProgressFraction(it))
			t.Resize(fyne.NewSize(posterW, posterH+60))
			cont.Add(container.NewGridWrap(fyne.NewSize(posterW, posterH+60), newTappable(t, func() { a.Push(NewDetail(a, it.ID, false)) })))
		}
		if len(cont.Objects) == 0 {
			contBox.Hide()
		} else {
			contBox.Show()
		}
		cont.Refresh()
	}

	search := widget.NewEntry()
	search.SetPlaceHolder("Search movies and shows")
	search.OnChanged = func(q string) { query = q; rebuild() }

	a.OnReload(rebuild)
	rebuild()
	return container.NewBorder(container.NewVBox(search, contBox), nil, nil, nil, grid)
}

// posterFor picks the poster to show for an item: its own, else its show's.
func posterFor(snap model.Snapshot, it model.PublicItem) *string {
	if art := vm.ArtFor(it.Poster, it.Backdrop); art != nil {
		return art
	}
	if it.Show != nil {
		for _, s := range snap.Shows {
			if s.Key == it.Show.Key {
				return vm.ArtFor(s.Poster, s.Backdrop)
			}
		}
	}
	return nil
}
