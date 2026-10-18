package library

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"homemedia/internal/config"
	"homemedia/internal/metadata"
)

func newFixtureLibrary(t *testing.T) *Library {
	t.Helper()
	root, _ := filepath.Abs("../../testdata/fixtures")
	if _, err := os.Stat(root); err != nil {
		t.Skip("run testdata/gen.sh first")
	}
	data := t.TempDir()
	cfg := &config.Config{MediaDir: root, DataDir: data, ArtDir: filepath.Join(data, "art")}
	if err := os.MkdirAll(cfg.ArtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(cfg, &metadata.Client{})
}

func TestScanSnapshot(t *testing.T) {
	l := newFixtureLibrary(t)
	if err := l.Scan(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	if len(snap.Items) != 5 { // sample.mkv is filtered out
		t.Fatalf("items = %d, want 5", len(snap.Items))
	}
	byTitle := map[string]int{}
	for i, it := range snap.Items {
		byTitle[it.Title] = i
	}
	direct := snap.Items[byTitle["Direct Movie"]]
	if direct.PlayMode != "direct" {
		t.Errorf("Direct Movie playMode = %s", direct.PlayMode)
	}
	if len(direct.Subs) != 2 {
		t.Errorf("Direct Movie subs = %+v, want 2 external", direct.Subs)
	}
	if hevc := snap.Items[byTitle["Hevc Movie"]]; hevc.PlayMode != "transcode" {
		t.Errorf("Hevc playMode = %s", hevc.PlayMode)
	}
	multi := snap.Items[byTitle["Multi Audio"]]
	if len(multi.Audio) != 2 || len(multi.Subs) != 2 {
		t.Errorf("Multi Audio audio=%d subs=%d, want 2 and 2", len(multi.Audio), len(multi.Subs))
	}
	if len(snap.Shows) != 1 || len(snap.Shows[0].Episodes) != 2 || snap.Shows[0].Title != "Test Show" {
		t.Errorf("shows = %+v", snap.Shows)
	}
	if direct.Backdrop == nil {
		t.Error("expected a thumbnail backdrop")
	}
}

func TestProgressWatchedAndPersistence(t *testing.T) {
	l := newFixtureLibrary(t)
	if err := l.Scan(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	id := l.Snapshot().Items[0].ID
	dur := l.Snapshot().Items[0].Duration

	l.SetProgress(id, 1800, 3600) // fixtures are shorter than the 120 s end window, so fake a long file
	if p := l.Snapshot().Items[0].Progress; p == nil || p.Watched || p.Pos != 1800 {
		t.Fatalf("mid progress = %+v", p)
	}
	l.SetProgress(id, dur-1, 0)
	if p := l.Snapshot().Items[0].Progress; p == nil || !p.Watched {
		t.Fatalf("end progress should be watched: %+v", p)
	}
	l.MarkWatched(id, false)
	if l.Snapshot().Items[0].Progress != nil {
		t.Error("unwatch should clear progress")
	}
	l.SetProgress(id, 1800, 3600)
	l.Flush()
	again := New(l.cfg, &metadata.Client{})
	if p := again.progress[id]; p.Pos == 0 {
		t.Error("progress did not persist")
	}
	if len(again.items) == 0 {
		t.Error("cache did not persist")
	}
}

func TestIDForAndSlug(t *testing.T) {
	if IDFor("a/b.mkv") != IDFor("a/b.mkv") || len(IDFor("x")) != 12 {
		t.Error("IDFor must be stable and 12 chars")
	}
	if got := Slug("Amélie: The Show!"); got != "amelie-the-show" {
		t.Errorf("Slug = %q", got)
	}
}

func TestNeedsLookup(t *testing.T) {
	now := time.Now().UnixMilli()
	old := now - 25*60*60*1000
	tests := []struct {
		name    string
		refresh bool
		triedAt int64
		hasMeta bool
		want    bool
	}{
		{"refresh re-fetches matched", true, now, true, true},
		{"refresh re-fetches untried", true, 0, false, true},
		{"matched without refresh is kept", false, old, true, false},
		{"unmatched and never tried", false, 0, false, true},
		{"unmatched and recently tried waits", false, now, false, false},
		{"unmatched and tried over a day ago retries", false, old, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsLookup(tt.refresh, tt.triedAt, tt.hasMeta); got != tt.want {
				t.Errorf("needsLookup = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindSubtitles(t *testing.T) {
	const root = "/media"
	files := []string{
		"/media/Show/ep1.mkv", "/media/Show/ep2.mkv",
		"/media/Show/ep1.en.srt", "/media/Show/ep1.srt", "/media/Show/ep2.srt",
		"/media/Movie/Movie.mkv", "/media/Movie/Extras/trailer.srt", "/media/Movie/Movie.eng.ass", "/media/Movie/notes.txt",
		"/media/Loose.mkv", "/media/other.srt",
	}
	videosByDir := map[string]int{}
	for _, f := range files {
		if VideoExts[strings.ToLower(filepath.Ext(f))] {
			videosByDir[filepath.Dir(f)]++
		}
	}
	idx := buildSubIndex(root, files)
	l := &Library{cfg: &config.Config{MediaDir: root}}
	tests := []struct {
		name  string
		video string
		want  []string
	}{
		{"same stem, shared folder", "/media/Show/ep1.mkv", []string{"/media/Show/ep1.en.srt", "/media/Show/ep1.srt"}},
		{"other video's sub not matched", "/media/Show/ep2.mkv", []string{"/media/Show/ep2.srt"}},
		{"alone in folder takes whole tree", "/media/Movie/Movie.mkv", []string{"/media/Movie/Extras/trailer.srt", "/media/Movie/Movie.eng.ass"}},
		{"root video does not take loose subs", "/media/Loose.mkv", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := l.findSubtitles(tt.video, idx, videosByDir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findSubtitles(%s) = %v, want %v", tt.video, got, tt.want)
			}
		})
	}
}
