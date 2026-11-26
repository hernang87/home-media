package library

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"homemedia/internal/config"
	"homemedia/internal/metadata"
	"homemedia/internal/model"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestCacheKey(t *testing.T) {
	tests := []struct{ name, want string }{
		{"abc123def456.poster.jpg", "abc123def456"},
		{"show-the-office.backdrop.jpg", "show-the-office"},
		{"abc123def456.i2.17f3-2a.vtt", "abc123def456"},
		{"abc123def456.json", "abc123def456"},
		{"abc123def456.json.tmp", "abc123def456"},
		{"noext", "noext"},
	}
	for _, tt := range tests {
		if got := cacheKey(tt.name); got != tt.want {
			t.Errorf("cacheKey(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestPruneCaches(t *testing.T) {
	data := t.TempDir()
	cfg := &config.Config{
		DataDir: data,
		ArtDir:  filepath.Join(data, "art"),
		SubDir:  filepath.Join(data, "subs"),
		KeyDir:  filepath.Join(data, "keyframes"),
	}
	l := New(cfg, &metadata.Client{})
	l.items = map[string]*model.Item{"aaaaaaaaaaaa": {ID: "aaaaaaaaaaaa"}}
	l.shows = map[string]*model.Show{"kept-show": {Key: "kept-show"}}

	for _, f := range []struct{ dir, name string }{
		{cfg.ArtDir, "aaaaaaaaaaaa.backdrop.jpg"},
		{cfg.ArtDir, "bbbbbbbbbbbb.backdrop.jpg"},
		{cfg.ArtDir, "show-kept-show.poster.jpg"},
		{cfg.ArtDir, "show-gone-show.poster.jpg"},
		{cfg.SubDir, "aaaaaaaaaaaa.i2.1-2.vtt"},
		{cfg.SubDir, "bbbbbbbbbbbb.i2.1-2.vtt"},
		{cfg.KeyDir, "aaaaaaaaaaaa.json"},
		{cfg.KeyDir, "bbbbbbbbbbbb.json"},
	} {
		touch(t, f.dir, f.name)
	}
	// Subdirectories are never touched.
	if err := os.Mkdir(filepath.Join(cfg.ArtDir, "bbbbbbbbbbbb.dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	l.pruneCaches()

	want := map[string][]string{
		cfg.ArtDir: {"aaaaaaaaaaaa.backdrop.jpg", "bbbbbbbbbbbb.dir", "show-kept-show.poster.jpg"},
		cfg.SubDir: {"aaaaaaaaaaaa.i2.1-2.vtt"},
		cfg.KeyDir: {"aaaaaaaaaaaa.json"},
	}
	for dir, names := range want {
		if got := listDir(t, dir); !slices.Equal(got, names) {
			t.Errorf("%s = %v, want %v", filepath.Base(dir), got, names)
		}
	}
}

func TestPruneDirMissing(t *testing.T) {
	if n := pruneDir(filepath.Join(t.TempDir(), "nope"), nil); n != 0 {
		t.Fatalf("pruneDir on missing dir = %d", n)
	}
	if n := pruneDir("", nil); n != 0 {
		t.Fatalf("pruneDir(\"\") = %d", n)
	}
}

func TestScanPrunesRemovedItems(t *testing.T) {
	video := fixtureVideo(t)
	l, media := newTempLibrary(t, &metadata.Client{})
	l.cfg.KeyDir = filepath.Join(l.cfg.DataDir, "keyframes")
	ctx := context.Background()

	keep := filepath.Join(media, "Keep.2020.mkv")
	gone := filepath.Join(media, "Gone.2020.mkv")
	copyFile(t, video, keep)
	copyFile(t, video, gone)
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	keepID, goneID := IDFor("Keep.2020.mkv"), IDFor("Gone.2020.mkv")
	touch(t, l.cfg.KeyDir, keepID+".json")
	touch(t, l.cfg.KeyDir, goneID+".json")

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, l.cfg.KeyDir); !slices.Equal(got, []string{keepID + ".json"}) {
		t.Errorf("keyframes = %v, want only %s", got, keepID)
	}
	if _, err := os.Stat(l.artPath(goneID, "backdrop")); !os.IsNotExist(err) {
		t.Errorf("thumbnail of removed item still there (err %v)", err)
	}
	if _, err := os.Stat(l.artPath(keepID, "backdrop")); err != nil {
		t.Errorf("thumbnail of kept item: %v", err)
	}

	// A scan that finds nothing (unmounted drive) keeps the caches.
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, l.cfg.KeyDir); len(got) != 1 {
		t.Errorf("empty scan pruned caches: %v", got)
	}
}
