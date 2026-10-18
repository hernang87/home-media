// Package vm holds the desktop app's view logic as plain functions, so it can be
// tested without a GUI.
package vm

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"homemedia/internal/model"
)

// Clock formats seconds as m:ss, or h:mm:ss from one hour on.
func Clock(sec float64) string { return model.Clock(sec) }

// ResumeLabel is the text of the "continue" choice.
func ResumeLabel(pos float64) string { return "Continue from " + Clock(pos) }

// Resumable reports whether an item is partly watched and worth offering "continue".
func Resumable(it model.PublicItem) bool {
	p := it.Progress
	return p != nil && !p.Watched && p.Pos > 0
}

// ContinueWatching returns partly watched items, most recently played first.
func ContinueWatching(items []model.PublicItem, limit int) []model.PublicItem {
	var out []model.PublicItem
	for _, it := range items {
		if Resumable(it) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Progress.T > out[j].Progress.T })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ArtFor picks the image for a tile: the poster, else the backdrop (servers
// without a TMDB key only have video frames).
func ArtFor(poster, backdrop *string) *string {
	if poster != nil && *poster != "" {
		return poster
	}
	return backdrop
}

// Shelf is one poster tile: a movie or a whole show.
type Shelf struct {
	Key      string // item ID for movies, show key for shows
	Title    string
	Year     *int
	Poster   *string
	IsShow   bool
	Watched  bool
	Added    float64
	Episodes int
}

// fold lowercases s and drops everything but letters and digits, so "Spider-Man",
// "Spider Man" and "spider.man" compare equal.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Searching reports whether a search query is active (not blank or punctuation only).
func Searching(query string) bool { return fold(query) != "" }

// Shelves lists movies and shows as poster tiles, newest first, optionally
// filtered by a title query that ignores case, spaces and punctuation.
func Shelves(snap model.Snapshot, query string) []Shelf {
	q := fold(query)
	match := func(title string) bool { return q == "" || strings.Contains(fold(title), q) }
	var out []Shelf
	for _, it := range snap.Items {
		if it.Type != model.TypeMovie || !match(it.Title) {
			continue
		}
		out = append(out, Shelf{Key: it.ID, Title: it.Title, Year: it.Year, Poster: ArtFor(it.Poster, it.Backdrop),
			Watched: it.Progress != nil && it.Progress.Watched, Added: it.Added})
	}
	for _, sh := range snap.Shows {
		if !match(sh.Title) {
			continue
		}
		out = append(out, Shelf{Key: sh.Key, Title: sh.Title, Year: sh.Year, Poster: ArtFor(sh.Poster, sh.Backdrop),
			IsShow: true, Added: sh.Added, Episodes: len(sh.Episodes)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Added > out[j].Added })
	return out
}

// Season is one season of a show with its episodes in order.
type Season struct {
	Number   int
	Episodes []model.PublicItem
}

// Seasons groups a show's episodes by season, in the order of show.Episodes.
func Seasons(snap model.Snapshot, show model.PublicShow) []Season {
	byID := make(map[string]model.PublicItem, len(snap.Items))
	for _, it := range snap.Items {
		byID[it.ID] = it
	}
	var out []Season
	for _, id := range show.Episodes {
		it, ok := byID[id]
		if !ok {
			continue
		}
		n := 0
		if it.Season != nil {
			n = *it.Season
		}
		if len(out) == 0 || out[len(out)-1].Number != n {
			out = append(out, Season{Number: n})
		}
		out[len(out)-1].Episodes = append(out[len(out)-1].Episodes, it)
	}
	return out
}

// EpisodeLabel is "S01E02  Title" (or just the title when numbers are missing).
func EpisodeLabel(it model.PublicItem) string {
	if it.Season == nil || it.Episode == nil {
		return it.Title
	}
	return fmt.Sprintf("S%02dE%02d  %s", *it.Season, *it.Episode, it.Title)
}

// ProgressFraction is how far through an item the viewer is, 0..1.
func ProgressFraction(it model.PublicItem) float64 {
	if it.Progress == nil || it.Duration <= 0 {
		return 0
	}
	if it.Progress.Watched {
		return 1
	}
	f := float64(it.Progress.Pos) / it.Duration
	if f > 1 {
		return 1
	}
	return f
}

// SubChoice is one entry of the subtitle menu.
type SubChoice struct {
	ID    string // "" = off
	Label string
}

// SubChoices returns "Off" followed by the item's subtitles.
func SubChoices(subs []model.PublicSub) []SubChoice {
	out := []SubChoice{{ID: "", Label: "Off"}}
	for _, s := range subs {
		label := s.Label
		if s.Forced && !strings.Contains(strings.ToLower(label), "forced") {
			label += " (forced)"
		}
		out = append(out, SubChoice{ID: s.ID, Label: label})
	}
	return out
}

// AudioLabel names an audio track for a menu: "1: English (aac, 5.1)".
func AudioLabel(i int, a model.AudioTrack) string {
	name := a.Lang
	if a.Title != "" {
		name = a.Title
	}
	if name == "" {
		name = "Track"
	}
	return fmt.Sprintf("%d: %s (%s, %s)", i+1, name, a.Codec, channelLayout(a.Channels))
}

func channelLayout(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%d ch", n)
}

// LogLine is one log entry as shown in the Logs tab: "15:04:05 WARN message".
func LogLine(t time.Time, level, msg string) string {
	return fmt.Sprintf("%s %-5s %s", t.Local().Format("15:04:05"), level, msg)
}

// LevelRank orders slog level names so the Logs tab can filter by severity.
func LevelRank(level string) int {
	switch {
	case strings.HasPrefix(level, "ERROR"):
		return 3
	case strings.HasPrefix(level, "WARN"):
		return 2
	case strings.HasPrefix(level, "INFO"):
		return 1
	}
	return 0 // DEBUG and anything unknown
}
