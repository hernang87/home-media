package ui

import (
	"context"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/ui/vm"
)

const maxLogLines = 1000

// logsScreen is the Logs tab: the server's recent log, refreshed every 2 s while
// the tab is on screen. Its fields are touched only on the UI thread.
type logsScreen struct {
	a       *App
	entries []client.LogEntry
	last    int // highest Seq seen
	minRank int // entries below this level are hidden
	follow  bool
	busy    bool

	rich   *widget.RichText
	scroll *container.Scroll
}

// NewLogs is the Logs tab.
func NewLogs(a *App) fyne.CanvasObject {
	l := &logsScreen{a: a, follow: true}
	l.rich = widget.NewRichText()
	l.rich.Wrapping = fyne.TextWrapWord
	l.scroll = container.NewVScroll(l.rich)

	levels := widget.NewSelect([]string{"All", "Info and up", "Warnings and errors"}, func(string) {})
	levels.OnChanged = func(string) {
		l.minRank = levels.SelectedIndex() // All=0 (debug), Info=1, Warn=2
		l.render()
	}
	levels.SetSelectedIndex(1)
	follow := widget.NewCheck("Follow", func(on bool) {
		l.follow = on
		if on {
			l.scroll.ScrollToBottom()
		}
	})
	follow.SetChecked(true)
	clear := widget.NewButtonWithIcon("Clear", theme.DeleteIcon(), func() {
		l.entries = nil
		l.render()
	})
	cp := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		a.Fyne.Clipboard().SetContent(l.text())
	})
	bar := container.NewBorder(nil, nil, nil, container.NewHBox(follow, clear, cp), levels)

	go func() {
		for range time.Tick(2 * time.Second) {
			fyne.Do(l.tick)
		}
	}()
	return container.NewBorder(bar, nil, nil, nil, l.scroll)
}

// tick polls the server when this tab is on screen.
func (l *logsScreen) tick() {
	if l.a.tabs.Selected() != l.a.logsTab || l.busy {
		return
	}
	l.busy = true
	after := l.last
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		entries, newest, err := l.a.Client.Logs(ctx, after)
		fyne.Do(func() {
			l.busy = false
			if err != nil {
				return
			}
			if newest < l.last { // the server restarted: its numbering began again
				l.entries, l.last = nil, 0
				return // the next tick fetches from the start
			}
			if len(entries) == 0 {
				return
			}
			l.entries = append(l.entries, entries...)
			if over := len(l.entries) - maxLogLines; over > 0 {
				l.entries = l.entries[over:]
			}
			l.last = entries[len(entries)-1].Seq
			l.render()
		})
	}()
}

func (l *logsScreen) visible() []client.LogEntry {
	var out []client.LogEntry
	for _, e := range l.entries {
		if vm.LevelRank(e.Level) >= l.minRank {
			out = append(out, e)
		}
	}
	return out
}

// text is the visible log as plain text, for the Copy button.
func (l *logsScreen) text() string {
	var s string
	for _, e := range l.visible() {
		s += vm.LogLine(e.Time, e.Level, e.Msg) + "\n"
	}
	return s
}

func (l *logsScreen) render() {
	var segs []widget.RichTextSegment
	for _, e := range l.visible() {
		style := widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}, ColorName: theme.ColorNameForeground}
		switch vm.LevelRank(e.Level) {
		case 3:
			style.ColorName = theme.ColorNameError
		case 2:
			style.ColorName = theme.ColorNameWarning
		case 0:
			style.ColorName = theme.ColorNameDisabled
		}
		segs = append(segs, &widget.TextSegment{Text: vm.LogLine(e.Time, e.Level, e.Msg), Style: style})
	}
	l.rich.Segments = segs
	l.rich.Refresh()
	if l.follow {
		l.scroll.ScrollToBottom()
	}
}
