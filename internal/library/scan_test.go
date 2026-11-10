package library

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homemedia/internal/config"
	"homemedia/internal/metadata"
	"homemedia/internal/model"
)

// fixtureVideo is a small real video to copy into temp media folders.
func fixtureVideo(t *testing.T) string {
	t.Helper()
	p, _ := filepath.Abs("../../testdata/fixtures/Shows/Test Show/Season 1/Test.Show.S01E01.mkv")
	if _, err := os.Stat(p); err != nil {
		t.Skip("run testdata/gen.sh first")
	}
	return p
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// newTempLibrary returns a library over an empty media folder.
func newTempLibrary(t *testing.T, meta *metadata.Client) (*Library, string) {
	t.Helper()
	media, data := t.TempDir(), t.TempDir()
	cfg := &config.Config{MediaDir: media, DataDir: data, ArtDir: filepath.Join(data, "art")}
	if err := os.MkdirAll(cfg.ArtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(cfg, meta), media
}

func titles(snap model.Snapshot) string {
	var out []string
	for _, it := range snap.Items {
		out = append(out, it.Title)
	}
	return strings.Join(out, ",")
}

func TestScanFollowsFileChanges(t *testing.T) {
	video := fixtureVideo(t)
	l, media := newTempLibrary(t, &metadata.Client{})
	ctx := context.Background()
	scan := func() model.Snapshot {
		t.Helper()
		if err := l.Scan(ctx, false); err != nil {
			t.Fatal(err)
		}
		return l.Snapshot()
	}
	if snap := scan(); len(snap.Items) != 0 || snap.Status.Scanning {
		t.Fatalf("empty folder: %+v", snap)
	}

	first := filepath.Join(media, "First.Movie.2001.mkv")
	copyFile(t, video, first)
	if snap := scan(); titles(snap) != "First Movie" {
		t.Fatalf("after adding one file: %q", titles(snap))
	}
	id := IDFor("First.Movie.2001.mkv")
	item, ok := l.Get(id)
	if !ok || item.Info == nil || item.Info.Video == nil || item.Path != first {
		t.Fatalf("Get = %+v, %v", item, ok)
	}
	added := item.Added

	// A hidden file, a hidden folder and a small "sample" are not library items.
	copyFile(t, video, filepath.Join(media, ".hidden.mkv"))
	copyFile(t, video, filepath.Join(media, ".trash", "Gone.2002.mkv"))
	copyFile(t, video, filepath.Join(media, "Second.Movie.2003", "sample.mkv"))
	copyFile(t, video, filepath.Join(media, "Second.Movie.2003", "Second.Movie.2003.mkv"))
	if snap := scan(); len(snap.Items) != 2 {
		t.Fatalf("after adding a folder: %q, want 2 items", titles(snap))
	}

	// A changed file is probed again but keeps its place in "recently added".
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(first, later, later); err != nil {
		t.Fatal(err)
	}
	scan()
	if item, _ := l.Get(id); item.Added != added || item.MTime == added || item.Info == nil {
		t.Errorf("after touching the file: added %v (want %v), mtime %v, info %v", item.Added, added, item.MTime, item.Info)
	}

	l.MarkWatched(id, true)
	if p, ok := l.Progress(id); !ok || !p.Watched || p.Dur == 0 {
		t.Errorf("progress after MarkWatched = %+v, %v", p, ok)
	}
	l.MarkWatched(id, false)
	l.MarkWatched("nope", true)
	l.SetProgress("nope", 10, 100)
	if _, ok := l.Progress(id); ok {
		t.Error("clearing the watched flag must drop the progress")
	}
	if _, ok := l.Progress("nope"); ok {
		t.Error("unknown items must not get progress")
	}

	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if snap := scan(); titles(snap) != "Second Movie" {
		t.Fatalf("after removing a file: %q", titles(snap))
	}
	if _, ok := l.Get(id); ok {
		t.Error("a removed file must leave the library")
	}

	// The cache survives a restart without probing anything again.
	l.Flush()
	again := New(l.cfg, &metadata.Client{})
	if snap := again.Snapshot(); titles(snap) != "Second Movie" {
		t.Errorf("reloaded library: %q", titles(snap))
	}
}

// fakeTMDB answers every movie search with one movie and every show search
// with one show, and counts the searches.
type fakeTMDB struct {
	mu       sync.Mutex
	searches int
	fail     bool
}

func (f *fakeTMDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches
}

func (f *fakeTMDB) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakeTMDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	fail := f.fail
	if strings.HasPrefix(r.URL.Path, "/search/") {
		f.searches++
	}
	f.mu.Unlock()
	if fail {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch p := r.URL.Path; {
	case p == "/search/movie":
		io.WriteString(w, `{"results":[{"id":7,"release_date":"2020-05-01"}]}`)
	case p == "/movie/7":
		io.WriteString(w, `{"id":7,"title":"Real Movie","release_date":"2020-05-01","overview":"A film.","vote_average":7.2,
			"genres":[{"name":"Drama"}],"runtime":101,"poster_path":"/p.jpg","backdrop_path":"/b.jpg"}`)
	case p == "/search/tv":
		io.WriteString(w, `{"results":[{"id":42,"first_air_date":"2015-01-01"}]}`)
	case p == "/tv/42":
		io.WriteString(w, `{"id":42,"name":"Real Show","first_air_date":"2015-01-01","overview":"A show.","poster_path":"/sp.jpg"}`)
	case p == "/tv/42/season/1":
		io.WriteString(w, `{"episodes":[{"episode_number":1,"name":"Pilot","overview":"It begins.","runtime":50,"still_path":"/s1.jpg"},
			{"episode_number":2,"name":"Second","still_path":"/missing.jpg"}]}`)
	case strings.HasPrefix(p, "/t/p/") && !strings.HasSuffix(p, "/missing.jpg"):
		w.Header().Set("Content-Type", "image/jpeg")
		io.WriteString(w, "JPEG")
	default:
		http.NotFound(w, r)
	}
}

func TestScanFetchesMetadata(t *testing.T) {
	video := fixtureVideo(t)
	tmdb := &fakeTMDB{}
	srv := httptest.NewServer(tmdb)
	defer srv.Close()
	meta := &metadata.Client{APIKey: "0123456789abcdef0123456789abcdef", APIBase: srv.URL, ImgBase: srv.URL + "/t/p"}
	l, media := newTempLibrary(t, meta)
	copyFile(t, video, filepath.Join(media, "Some.Movie.2020.mkv"))
	copyFile(t, video, filepath.Join(media, "Some Show", "Season 1", "Some.Show.S01E01.mkv"))
	copyFile(t, video, filepath.Join(media, "Some Show", "Season 1", "Some.Show.S01E02.mkv"))
	ctx := context.Background()

	// TMDB is down: the scan still lists everything, with frames as artwork.
	tmdb.setFail(true)
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	if len(snap.Items) != 3 || len(snap.Shows) != 1 || snap.Shows[0].Title != "Some Show" {
		t.Fatalf("scan with TMDB down: %q, shows %+v", titles(snap), snap.Shows)
	}
	for _, it := range snap.Items {
		if it.Poster != nil || it.Backdrop == nil {
			t.Errorf("%s: poster %v backdrop %v, want only a frame backdrop", it.File, it.Poster, it.Backdrop)
		}
	}

	// TMDB is back: the failed lookups are retried on the next scan.
	tmdb.setFail(false)
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	snap = l.Snapshot()
	byFile := map[string]model.PublicItem{}
	for _, it := range snap.Items {
		byFile[it.File] = it
	}
	movie := byFile["Some.Movie.2020.mkv"]
	if movie.Title != "Real Movie" || movie.Overview != "A film." || movie.Runtime != 101 || movie.Poster == nil || len(movie.Genres) != 1 {
		t.Errorf("movie = %+v", movie)
	}
	if _, err := os.Stat(filepath.Join(l.cfg.ArtDir, movie.ID+".poster.jpg")); err != nil {
		t.Errorf("poster not downloaded: %v", err)
	}
	show := snap.Shows[0]
	if show.Title != "Real Show" || show.Overview != "A show." || show.Poster == nil || len(show.Episodes) != 2 {
		t.Errorf("show = %+v", show)
	}
	ep1, ep2 := byFile["Some.Show.S01E01.mkv"], byFile["Some.Show.S01E02.mkv"]
	if ep1.Title != "Pilot" || ep1.Overview != "It begins." || ep1.Show == nil || ep1.Show.Title != "Real Show" {
		t.Errorf("episode 1 = %+v", ep1)
	}
	if ep2.Title != "Second" || ep2.Backdrop == nil {
		t.Errorf("episode 2 = %+v, want its title and the frame kept as backdrop", ep2)
	}

	// Matched entries are not looked up again, except on a refresh.
	before := tmdb.count()
	if err := l.Scan(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := tmdb.count(); got != before {
		t.Errorf("a plain rescan searched TMDB %d more times", got-before)
	}
	if err := l.Scan(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := tmdb.count(); got != before+2 {
		t.Errorf("a refresh searched %d times, want 2 (the movie and the show)", got-before)
	}
}

func TestNeedsLookupRetriesUnmatchedAfterADay(t *testing.T) {
	old := time.Now().Add(-25 * time.Hour).UnixMilli()
	recent := time.Now().Add(-time.Hour).UnixMilli()
	if !needsLookup(false, old, false) || needsLookup(false, recent, false) {
		t.Error("an unmatched entry is retried only after retryMeta")
	}
}

func TestWatchRescansOnNewFiles(t *testing.T) {
	video := fixtureVideo(t)
	old := watchDebounce
	watchDebounce = 200 * time.Millisecond
	t.Cleanup(func() { watchDebounce = old })

	l, media := newTempLibrary(t, &metadata.Client{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Watch(ctx) }()
	time.Sleep(200 * time.Millisecond) // let the watcher register the folder

	// A file in a folder created after the watch began must be seen too.
	copyFile(t, video, filepath.Join(media, "New.Movie.2022", "New.Movie.2022.mkv"))
	deadline := time.Now().Add(30 * time.Second)
	for len(l.Snapshot().Items) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the new file never reached the library")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Watch returned %v", err)
	}
}
