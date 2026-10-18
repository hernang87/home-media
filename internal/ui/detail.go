package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/model"
	"homemedia/internal/ui/vm"
)

// NewDetail shows one movie (isShow false, key = item ID) or one show (isShow
// true, key = show key).
func NewDetail(a *App, key string, isShow bool) fyne.CanvasObject {
	back := widget.NewButtonWithIcon("Back", theme.NavigateBackIcon(), a.Pop)
	back.Importance = widget.LowImportance
	var body fyne.CanvasObject
	if isShow {
		body = showBody(a, key)
	} else {
		body = movieBody(a, key)
	}
	return container.NewVScroll(container.NewVBox(container.NewHBox(back), body))
}

// movieBody is the header, the playback controls and the details of one movie or episode.
func movieBody(a *App, key string) fyne.CanvasObject {
	it, ok := a.Item(key)
	if !ok {
		return widget.NewLabel("Item not found")
	}
	mode := "Transcode"
	if it.PlayMode == "direct" {
		mode = "Direct Play"
	}
	items := []fyne.CanvasObject{
		newBackdrop(a, it.Backdrop),
		titleLabel(it.Title, it.Year),
		widget.NewLabel(metaLine(it.Rating, it.Runtime, it.Genres)),
		widget.NewLabel(mode),
	}

	// The audio and subtitle choices are slice positions, read back with SelectedIndex.
	audioIdx := 0
	for i, t := range it.Audio {
		if t.Default {
			audioIdx = i
			break
		}
	}
	if len(it.Audio) > 1 {
		opts := make([]string, len(it.Audio))
		for i, t := range it.Audio {
			opts[i] = vm.AudioLabel(i, t)
		}
		sel := widget.NewSelect(opts, nil)
		sel.SetSelectedIndex(audioIdx)
		sel.OnChanged = func(string) { audioIdx = sel.SelectedIndex() }
		items = append(items, container.NewBorder(nil, nil, widget.NewLabel("Audio"), nil, sel))
	}

	choices := vm.SubChoices(it.Subs)
	subOpts := make([]string, len(choices))
	for i, c := range choices {
		subOpts[i] = c.Label
	}
	subID := ""
	subSel := widget.NewSelect(subOpts, nil)
	subSel.SetSelectedIndex(0)
	subSel.OnChanged = func(string) { subID = choices[subSel.SelectedIndex()].ID }
	items = append(items, container.NewBorder(nil, nil, widget.NewLabel("Subtitles"), nil, subSel))

	play := widget.NewButton(playLabel(it), func() { a.PlayItem(it, audioIdx, subID) })

	watched := it.Progress != nil && it.Progress.Watched
	var mark *widget.Button
	mark = widget.NewButton(markLabel(watched), func() {
		target := !watched
		mark.Disable()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := a.Client.SetWatched(ctx, it.ID, target); err != nil {
				fyne.Do(mark.Enable)
				a.ShowError(fmt.Errorf("could not update %q: %w", it.Title, err))
				return
			}
			a.Reload()
			fyne.Do(func() {
				watched = target
				mark.SetText(markLabel(watched))
				mark.Enable()
			})
		}()
	})
	items = append(items, container.NewHBox(play, mark))

	if it.Tagline != "" {
		tag := wrapped(it.Tagline)
		tag.TextStyle = fyne.TextStyle{Italic: true}
		items = append(items, tag)
	}
	items = append(items, wrapped(it.Overview))
	if len(it.Cast) > 0 {
		var names []string
		for i, c := range it.Cast {
			if i == 8 {
				break
			}
			if c.Role != "" {
				names = append(names, c.Name+" ("+c.Role+")")
			} else {
				names = append(names, c.Name)
			}
		}
		items = append(items, wrapped(strings.Join(names, ", ")))
	}
	return container.NewVBox(items...)
}

// showBody is the header of one show and its seasons as an accordion of episodes.
func showBody(a *App, key string) fyne.CanvasObject {
	show, ok := a.Show(key)
	if !ok {
		return widget.NewLabel("Show not found")
	}
	items := []fyne.CanvasObject{
		newBackdrop(a, show.Backdrop),
		titleLabel(show.Title, show.Year),
		widget.NewLabel(metaLine(show.Rating, 0, show.Genres)),
		wrapped(show.Overview),
	}

	var accItems []*widget.AccordionItem
	open := -1
	for i, s := range vm.Seasons(a.Snapshot(), show) {
		title := fmt.Sprintf("Season %d", s.Number)
		if s.Number == 0 {
			title = "Specials"
		}
		var rows []fyne.CanvasObject
		unwatched := false
		for _, ep := range s.Episodes {
			ep := ep
			if ep.Progress == nil || !ep.Progress.Watched {
				unwatched = true
			}
			row := container.NewHBox(widget.NewLabel(vm.EpisodeLabel(ep)))
			if ep.Progress != nil && ep.Progress.Watched {
				row.Add(smallLabel("watched"))
			} else if vm.Resumable(ep) {
				row.Add(smallLabel(vm.Clock(float64(ep.Progress.Pos))))
			}
			rows = append(rows, newTappable(row, func() { a.Push(NewDetail(a, ep.ID, false)) }))
		}
		if open < 0 && unwatched {
			open = i
		}
		accItems = append(accItems, widget.NewAccordionItem(title, container.NewVBox(rows...)))
	}
	acc := widget.NewAccordion(accItems...)
	if open >= 0 {
		acc.Open(open)
	}
	items = append(items, acc)
	return container.NewVBox(items...)
}

// newBackdrop shows the backdrop image, loaded through the art cache.
func newBackdrop(a *App, rel *string) *canvas.Image {
	img := canvas.NewImageFromResource(theme.FileVideoIcon())
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(0, 220))
	a.Art.Get(rel, func(res fyne.Resource) {
		img.Resource = res
		img.Refresh()
	})
	return img
}

// titleLabel is the bold title with the year after it.
func titleLabel(title string, year *int) *widget.Label {
	if year != nil {
		title = fmt.Sprintf("%s (%d)", title, *year)
	}
	return widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// metaLine joins rating, runtime in minutes and genres with " · ", skipping empty parts.
func metaLine(rating *float64, runtime int, genres []string) string {
	var parts []string
	if rating != nil {
		parts = append(parts, fmt.Sprintf("%.1f/10", *rating))
	}
	if runtime > 0 {
		parts = append(parts, fmt.Sprintf("%d min", runtime))
	}
	if len(genres) > 0 {
		parts = append(parts, strings.Join(genres, ", "))
	}
	return strings.Join(parts, " · ")
}

// wrapped is a label that wraps long text at word boundaries.
func wrapped(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

// smallLabel is a caption-sized label for secondary text.
func smallLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.SizeName = theme.SizeNameCaptionText
	return l
}

// playLabel is "Resume" when the item has a saved position worth resuming.
func playLabel(it model.PublicItem) string {
	if vm.Resumable(it) {
		return "Resume"
	}
	return "Play"
}

func markLabel(watched bool) string {
	if watched {
		return "Mark unwatched"
	}
	return "Mark watched"
}
