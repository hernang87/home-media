package vm

import (
	"testing"
	"time"

	"homemedia/internal/model"
)

func ip(n int) *int { return &n }

func movie(id, title string, added float64, pr *model.PublicProgress) model.PublicItem {
	return model.PublicItem{ID: id, Type: model.TypeMovie, Title: title, Added: added, Duration: 100, Progress: pr}
}

func TestClock(t *testing.T) {
	for in, want := range map[float64]string{0: "0:00", 35: "0:35", 1500: "25:00", 3600: "1:00:00", 7265: "2:01:05", -5: "0:00"} {
		if got := Clock(in); got != want {
			t.Errorf("Clock(%v) = %q, want %q", in, got, want)
		}
	}
	if got := ResumeLabel(1500); got != "Continue from 25:00" {
		t.Errorf("ResumeLabel = %q", got)
	}
}

func TestContinueWatching(t *testing.T) {
	items := []model.PublicItem{
		movie("a", "A", 1, &model.PublicProgress{Pos: 40, T: 100}),
		movie("b", "B", 2, &model.PublicProgress{Pos: 40, T: 300}),
		movie("c", "C", 3, &model.PublicProgress{Pos: 0, T: 400}),
		movie("d", "D", 4, &model.PublicProgress{Pos: 40, Watched: true, T: 500}),
		movie("e", "E", 5, nil),
		movie("f", "F", 6, &model.PublicProgress{Pos: 10, T: 200}),
	}
	got := ContinueWatching(items, 0)
	if len(got) != 3 || got[0].ID != "b" || got[1].ID != "f" || got[2].ID != "a" {
		t.Fatalf("order = %v", ids(got))
	}
	if got := ContinueWatching(items, 2); len(got) != 2 {
		t.Fatalf("limit ignored: %v", ids(got))
	}
}

func ids(items []model.PublicItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestShelvesAndSeasons(t *testing.T) {
	s1, s2 := ip(1), ip(2)
	e := func(id string, season *int, ep int) model.PublicItem {
		n := ep
		return model.PublicItem{ID: id, Type: model.TypeEpisode, Title: "T" + id, Season: season, Episode: &n}
	}
	snap := model.Snapshot{
		Items: []model.PublicItem{
			movie("m1", "Alien", 10, nil), movie("m2", "Blade Runner", 30, &model.PublicProgress{Watched: true}),
			e("e1", s1, 1), e("e2", s1, 2), e("e3", s2, 1),
		},
		Shows: []model.PublicShow{{Key: "show", Title: "The Show", Added: 20, Episodes: []string{"e1", "e2", "e3", "gone"}}},
	}
	sh := Shelves(snap, "")
	if len(sh) != 3 || sh[0].Key != "m2" || sh[1].Key != "show" || !sh[1].IsShow || sh[1].Episodes != 4 || !sh[0].Watched {
		t.Fatalf("shelves = %+v", sh)
	}
	if got := Shelves(snap, "  ALI "); len(got) != 1 || got[0].Key != "m1" {
		t.Fatalf("query = %+v", got)
	}
	seasons := Seasons(snap, snap.Shows[0])
	if len(seasons) != 2 || seasons[0].Number != 1 || len(seasons[0].Episodes) != 2 || len(seasons[1].Episodes) != 1 {
		t.Fatalf("seasons = %+v", seasons)
	}
	if got := EpisodeLabel(snap.Items[2]); got != "S01E01  Te1" {
		t.Errorf("EpisodeLabel = %q", got)
	}
	if got := EpisodeLabel(snap.Items[0]); got != "Alien" {
		t.Errorf("EpisodeLabel movie = %q", got)
	}
}

func TestProgressFraction(t *testing.T) {
	if f := ProgressFraction(movie("a", "A", 0, nil)); f != 0 {
		t.Error(f)
	}
	if f := ProgressFraction(movie("a", "A", 0, &model.PublicProgress{Pos: 25})); f != 0.25 {
		t.Error(f)
	}
	if f := ProgressFraction(movie("a", "A", 0, &model.PublicProgress{Pos: 500})); f != 1 {
		t.Error(f)
	}
	if f := ProgressFraction(movie("a", "A", 0, &model.PublicProgress{Watched: true})); f != 1 {
		t.Error(f)
	}
}

func TestSubAndAudioLabels(t *testing.T) {
	c := SubChoices([]model.PublicSub{{ID: "e0", Label: "English"}, {ID: "i0", Label: "Spanish", Forced: true}, {ID: "i1", Label: "French forced", Forced: true}})
	if len(c) != 4 || c[0].ID != "" || c[0].Label != "Off" || c[2].Label != "Spanish (forced)" || c[3].Label != "French forced" {
		t.Fatalf("choices = %+v", c)
	}
	if got := AudioLabel(0, model.AudioTrack{Codec: "aac", Channels: 6, Lang: "eng"}); got != "1: eng (aac, 5.1)" {
		t.Errorf("AudioLabel = %q", got)
	}
	if got := AudioLabel(1, model.AudioTrack{Codec: "ac3", Channels: 3, Title: "Commentary"}); got != "2: Commentary (ac3, 3 ch)" {
		t.Errorf("AudioLabel = %q", got)
	}
}

func TestArtFor(t *testing.T) {
	p, b, empty := "/p", "/b", ""
	if got := ArtFor(&p, &b); got != &p {
		t.Error("poster should win")
	}
	if got := ArtFor(nil, &b); got != &b {
		t.Error("nil poster should fall back to backdrop")
	}
	if got := ArtFor(&empty, &b); got != &b {
		t.Error("empty poster should fall back to backdrop")
	}
	if got := ArtFor(nil, nil); got != nil {
		t.Error("no art should stay nil")
	}
}

func TestLogLineAndRank(t *testing.T) {
	ts := time.Date(2026, 10, 17, 15, 4, 5, 0, time.Local)
	if got := LogLine(ts, "WARN", "tv gone"); got != "15:04:05 WARN  tv gone" {
		t.Errorf("LogLine = %q", got)
	}
	if !(LevelRank("ERROR") > LevelRank("WARN") && LevelRank("WARN") > LevelRank("INFO") && LevelRank("INFO") > LevelRank("DEBUG")) {
		t.Error("level ranks out of order")
	}
	if LevelRank("INFO+2") != 1 || LevelRank("???") != 0 {
		t.Error("offset and unknown levels")
	}
}

func TestShelvesQueryIgnoresPunctuation(t *testing.T) {
	snap := model.Snapshot{Items: []model.PublicItem{
		{ID: "a", Type: model.TypeMovie, Title: "Spider-Man Brand New Day"},
		{ID: "b", Type: model.TypeMovie, Title: "Spider Man 2"},
		{ID: "c", Type: model.TypeMovie, Title: "Spiderman"},
		{ID: "d", Type: model.TypeMovie, Title: "The Invite"},
	}}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"spider-man", 3}, {"Spider Man", 3}, {"spider.man", 3}, {"  spiderman ", 3},
		{"man 2", 1}, {"invite", 1}, {"-", 4}, {"", 4}, {"zzz", 0},
	} {
		if got := Shelves(snap, tc.query); len(got) != tc.want {
			t.Errorf("Shelves(%q) = %d tiles, want %d", tc.query, len(got), tc.want)
		}
	}
}

func TestSearching(t *testing.T) {
	for q, want := range map[string]bool{"": false, "  ": false, "-": false, "a": true, " Spider-Man ": true} {
		if got := Searching(q); got != want {
			t.Errorf("Searching(%q) = %v, want %v", q, got, want)
		}
	}
}
