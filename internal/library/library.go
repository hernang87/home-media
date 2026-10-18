// Package library scans the media folder, keeps the item cache and watch
// progress, and builds the API snapshot.
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"homemedia/internal/config"
	"homemedia/internal/metadata"
	"homemedia/internal/model"
	"homemedia/internal/parse"
	"homemedia/internal/probe"
	"homemedia/internal/subs"
)

const retryMetaMS = 24 * 60 * 60 * 1000

var sampleRe = regexp.MustCompile(`(?i)\bsample\b`)

type cacheFile struct {
	Items map[string]*model.Item `json:"items"`
	Shows map[string]*model.Show `json:"shows"`
}

type stateFile struct {
	Progress map[string]model.Progress `json:"progress"`
}

// Library is safe for concurrent use.
type Library struct {
	cfg  *config.Config
	meta *metadata.Client

	mu       sync.RWMutex
	items    map[string]*model.Item
	shows    map[string]*model.Show
	progress map[string]model.Progress
	status   model.ScanStatus

	artMu  sync.Mutex       // guards artVer; may be taken under mu, so never take mu while holding it
	artVer map[string]int64 // art path -> mtime (unix seconds), 0 if missing

	scanMu    sync.Mutex // held while a scan runs
	pending   *bool      // queued rescan (refresh flag), guarded by mu
	saveSoon  *debouncer
	stateSoon *debouncer
}

// New loads the cache and progress files from cfg.DataDir. meta may be a
// disabled client (no API key).
func New(cfg *config.Config, meta *metadata.Client) *Library {
	l := &Library{cfg: cfg, meta: meta, items: map[string]*model.Item{}, shows: map[string]*model.Show{}, progress: map[string]model.Progress{}, artVer: map[string]int64{}}
	var c cacheFile
	if readJSON(l.cachePath(), &c) == nil {
		if c.Items != nil {
			l.items = c.Items
		}
		if c.Shows != nil {
			l.shows = c.Shows
		}
	}
	var s stateFile
	if readJSON(l.statePath(), &s) == nil && s.Progress != nil {
		l.progress = s.Progress
	}
	l.saveSoon = newDebouncer(time.Second, l.saveCache)
	l.stateSoon = newDebouncer(time.Second, l.saveState)
	return l
}

func (l *Library) cachePath() string { return filepath.Join(l.cfg.DataDir, "library.json") }
func (l *Library) statePath() string { return filepath.Join(l.cfg.DataDir, "state.json") }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) {
	b, err := json.Marshal(v)
	if err == nil {
		// Write then rename so a crash never leaves a truncated file.
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		slog.Error("save failed", "file", path, "err", err)
	}
}

func (l *Library) saveCache() {
	l.mu.RLock()
	defer l.mu.RUnlock()
	writeJSON(l.cachePath(), cacheFile{Items: l.items, Shows: l.shows})
}

func (l *Library) saveState() {
	l.mu.RLock()
	defer l.mu.RUnlock()
	writeJSON(l.statePath(), stateFile{Progress: l.progress})
}

// Flush writes cache and progress immediately (call on shutdown).
func (l *Library) Flush() {
	l.saveCache()
	l.saveState()
}

// Get returns a copy of the item with this ID.
func (l *Library) Get(id string) (model.Item, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	it, ok := l.items[id]
	if !ok {
		return model.Item{}, false
	}
	return *it, true
}

// Status returns the current scan status.
func (l *Library) Status() model.ScanStatus {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.status
}

func (l *Library) setStatus(f func(s *model.ScanStatus)) {
	l.mu.Lock()
	f(&l.status)
	l.mu.Unlock()
}

func (l *Library) bump() { l.setStatus(func(s *model.ScanStatus) { s.Done++ }) }

func walk(dir string, out *[]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := walk(full, out); err != nil {
				slog.Warn("cannot read directory", "dir", full, "err", err)
			}
		} else if e.Type().IsRegular() {
			*out = append(*out, full)
		}
	}
	return nil
}

// subIndex holds subtitle files by folder so each video finds its candidates
// without scanning every file. direct lists the subs in a folder; tree lists
// the subs at or below it (under the media root). Both keep walk order.
type subIndex struct {
	direct map[string][]string
	tree   map[string][]string
}

func buildSubIndex(root string, files []string) subIndex {
	idx := subIndex{direct: map[string][]string{}, tree: map[string][]string{}}
	for _, f := range files {
		if !SubExts[strings.ToLower(filepath.Ext(f))] {
			continue
		}
		dir := filepath.Dir(f)
		idx.direct[dir] = append(idx.direct[dir], f)
		for d := dir; d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
			idx.tree[d] = append(idx.tree[d], f)
		}
	}
	return idx
}

// findSubtitles returns subtitle files named after the video, or any in its own
// folder tree when it is the only video there.
func (l *Library) findSubtitles(video string, idx subIndex, videosByDir map[string]int) []string {
	dir := filepath.Dir(video)
	if dir != l.cfg.MediaDir && videosByDir[dir] == 1 {
		return idx.tree[dir]
	}
	stem := strings.ToLower(norm.NFC.String(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video))))
	var out []string
	for _, f := range idx.direct[dir] {
		if strings.HasPrefix(strings.ToLower(norm.NFC.String(filepath.Base(f))), stem) {
			out = append(out, f)
		}
	}
	return out
}

func buildSubs(item *model.Item) []model.SubTrack {
	var out []model.SubTrack
	for i, f := range item.SubFiles {
		d := subs.DescribeSubFile(f)
		out = append(out, model.SubTrack{ID: fmt.Sprintf("e%d", i), Source: "external", File: f, Lang: d.Lang, Label: subs.SubLabel(d), Forced: d.Forced})
	}
	if item.Info != nil {
		for _, s := range item.Info.Subs {
			if !s.Text {
				continue
			}
			label := subs.SubLabel(model.SubDescription{Lang: s.Lang, Title: s.Title, Forced: s.Forced})
			out = append(out, model.SubTrack{ID: fmt.Sprintf("i%d", s.Index), Source: "embedded", Index: s.Index, Lang: s.Lang, Label: label, Forced: s.Forced})
		}
	}
	return out
}

// Scan walks the media folder and updates the cache. If a scan is already
// running the request is queued and runs when it ends.
func (l *Library) Scan(ctx context.Context, refresh bool) error {
	if !l.scanMu.TryLock() {
		l.mu.Lock()
		r := refresh || (l.pending != nil && *l.pending)
		l.pending = &r
		l.mu.Unlock()
		return nil
	}
	err := l.scanOnce(ctx, refresh)
	// Reset before unlocking so a queued scan that starts next cannot have its status clobbered.
	l.setStatus(func(s *model.ScanStatus) { *s = model.ScanStatus{} })
	l.scanMu.Unlock()

	l.mu.Lock()
	next := l.pending
	l.pending = nil
	l.mu.Unlock()
	if next != nil && ctx.Err() == nil {
		return l.Scan(ctx, *next)
	}
	return err
}

func (l *Library) scanOnce(ctx context.Context, refresh bool) error {
	l.setStatus(func(s *model.ScanStatus) { *s = model.ScanStatus{Scanning: true, Phase: "Looking for files"} })
	var files []string
	if err := walk(l.cfg.MediaDir, &files); err != nil {
		return err
	}
	var videos []string
	videosByDir := map[string]int{}
	for _, f := range files {
		if VideoExts[strings.ToLower(filepath.Ext(f))] {
			videos = append(videos, f)
			videosByDir[filepath.Dir(f)]++
		}
	}
	idx := buildSubIndex(l.cfg.MediaDir, files)
	if refresh {
		l.artMu.Lock()
		l.artVer = map[string]int64{}
		l.artMu.Unlock()
	}

	// Stat and match subtitles without the lock; only the item updates need it.
	type scanned struct {
		file, rel, id string
		st            os.FileInfo
		subFiles      []string
	}
	var found []scanned
	for _, file := range videos {
		st, err := os.Stat(file)
		if err != nil {
			continue
		}
		// Skip trailers and samples bundled with releases.
		if sampleRe.MatchString(filepath.Base(file)) && st.Size() < 200*1024*1024 {
			continue
		}
		rel, _ := filepath.Rel(l.cfg.MediaDir, file)
		found = append(found, scanned{file: file, rel: rel, id: IDFor(rel), st: st, subFiles: l.findSubtitles(file, idx, videosByDir)})
	}

	seen := map[string]bool{}
	var toProbe []*model.Item
	l.mu.Lock()
	for _, f := range found {
		seen[f.id] = true
		mtime := float64(f.st.ModTime().UnixMilli())
		item := l.items[f.id]
		changed := item == nil || item.Size != f.st.Size() || item.MTime != mtime
		if item == nil {
			item = &model.Item{ID: f.id}
			l.items[f.id] = item
		}
		item.Path, item.Rel, item.Size, item.MTime = f.file, f.rel, f.st.Size(), mtime
		if item.Added == 0 {
			item.Added = mtime
		}
		item.Parsed = parse.ParseMediaPath(f.rel)
		item.SubFiles = f.subFiles
		if changed || item.Info == nil {
			toProbe = append(toProbe, item)
		}
	}
	for id := range l.items {
		if !seen[id] {
			delete(l.items, id)
		}
	}
	l.status = model.ScanStatus{Scanning: true, Phase: "Analyzing media", Total: len(toProbe)}
	l.mu.Unlock()

	mapLimit(toProbe, 3, func(item *model.Item) {
		defer l.bump()
		info, err := probe.Probe(ctx, item.Path)
		if err != nil {
			slog.Error("cannot read media", "file", item.Rel, "err", err)
		}
		l.mu.Lock()
		item.Info = info
		l.mu.Unlock()
	})

	l.mu.Lock()
	caps := probe.CapsForModel("")
	for _, item := range l.items {
		if item.Info == nil {
			continue
		}
		item.DirectPlay = probe.CanDirectPlay(item.Path, item.Info, caps)
		item.Subs = buildSubs(item)
	}
	// Shows are derived from episodes' parsed names.
	keep := map[string]bool{}
	for _, item := range l.items {
		if item.Parsed.Type != model.TypeEpisode {
			continue
		}
		key := Slug(item.Parsed.Show)
		keep[key] = true
		if l.shows[key] == nil {
			l.shows[key] = &model.Show{Key: key, Title: item.Parsed.Show, Year: item.Parsed.ShowYear}
		}
	}
	for key := range l.shows {
		if !keep[key] {
			delete(l.shows, key)
		}
	}
	l.mu.Unlock()

	if l.meta != nil && l.meta.Enabled() {
		l.enrich(ctx, refresh)
	}
	l.makeThumbnails()
	l.saveSoon.Trigger()
	return ctx.Err()
}

func stale(refresh bool, triedAt int64, hasMeta bool) bool {
	return refresh || triedAt == 0 || (!hasMeta && time.Now().UnixMilli()-triedAt > retryMetaMS)
}

// needsLookup reports whether an item or show should be sent to TMDB. A refresh
// re-fetches everything; otherwise only unmatched entries that are stale.
func needsLookup(refresh bool, triedAt int64, hasMeta bool) bool {
	return refresh || (!hasMeta && stale(false, triedAt, false))
}

func (l *Library) artPath(key, kind string) string {
	return filepath.Join(l.cfg.ArtDir, fmt.Sprintf("%s.%s.jpg", key, kind))
}

// invalidateArt forgets the cached mtime of art files the library just wrote.
func (l *Library) invalidateArt(paths ...string) {
	l.artMu.Lock()
	defer l.artMu.Unlock()
	for _, p := range paths {
		delete(l.artVer, p)
	}
}

// fetchArt downloads the poster and backdrop of a TMDB match into key's art
// files. An error means a download failed, not that TMDB has no image.
func (l *Library) fetchArt(ctx context.Context, meta *model.Meta, key string) (model.Art, error) {
	pPath, bPath := l.artPath(key, "poster"), l.artPath(key, "backdrop")
	defer l.invalidateArt(pPath, bPath)
	poster, err1 := l.meta.DownloadImage(ctx, meta.PosterPath, "w500", pPath)
	backdrop, err2 := l.meta.DownloadImage(ctx, meta.BackdropPath, "w1280", bPath)
	return model.Art{Poster: poster, Backdrop: backdrop}, errors.Join(err1, err2)
}

// enrichMovie stores the TMDB match for a movie. A failed lookup or download
// leaves Meta and TriedAt alone so the next scan retries it.
func (l *Library) enrichMovie(ctx context.Context, it *model.Item) {
	meta, err := l.meta.LookupMovie(ctx, it.Parsed.Title, it.Parsed.Year)
	if err != nil {
		slog.Warn("movie metadata failed", "title", it.Parsed.Title, "err", err)
		return
	}
	var art model.Art
	if meta != nil {
		if art, err = l.fetchArt(ctx, meta, it.ID); err != nil {
			slog.Warn("movie artwork failed", "title", it.Parsed.Title, "err", err)
			return
		}
	}
	l.mu.Lock()
	it.TriedAt = time.Now().UnixMilli()
	if meta != nil {
		it.Meta, it.Art = meta, art
	}
	l.mu.Unlock()
}

// enrichShow is enrichMovie for a show.
func (l *Library) enrichShow(ctx context.Context, s *model.Show) {
	meta, err := l.meta.LookupShow(ctx, s.Title, s.Year)
	if err != nil {
		slog.Warn("show metadata failed", "title", s.Title, "err", err)
		return
	}
	var art model.Art
	if meta != nil {
		if art, err = l.fetchArt(ctx, meta, "show-"+s.Key); err != nil {
			slog.Warn("show artwork failed", "title", s.Title, "err", err)
			return
		}
	}
	l.mu.Lock()
	s.TriedAt = time.Now().UnixMilli()
	if meta != nil {
		s.Meta, s.Art = meta, art
	}
	l.mu.Unlock()
}

func (l *Library) enrich(ctx context.Context, refresh bool) {
	l.mu.RLock()
	var movies []*model.Item
	for _, it := range l.items {
		if it.Info != nil && it.Parsed.Type == model.TypeMovie && needsLookup(refresh, it.TriedAt, it.Meta != nil) {
			movies = append(movies, it)
		}
	}
	var shows []*model.Show
	for _, s := range l.shows {
		if needsLookup(refresh, s.TriedAt, s.Meta != nil) {
			shows = append(shows, s)
		}
	}
	l.mu.RUnlock()
	l.setStatus(func(s *model.ScanStatus) {
		*s = model.ScanStatus{Scanning: true, Phase: "Fetching metadata", Total: len(movies) + len(shows)}
	})

	mapLimit(movies, 4, func(it *model.Item) {
		defer l.bump()
		l.enrichMovie(ctx, it)
	})
	for _, s := range shows {
		l.enrichShow(ctx, s)
		l.bump()
	}
	l.mu.RLock()
	var withMeta []*model.Show
	for _, s := range l.shows {
		if s.Meta != nil && s.Meta.TmdbID != 0 {
			withMeta = append(withMeta, s)
		}
	}
	l.mu.RUnlock()
	for _, s := range withMeta {
		l.enrichEpisodes(ctx, s, refresh)
	}
}

func (l *Library) enrichEpisodes(ctx context.Context, show *model.Show, refresh bool) {
	l.mu.RLock()
	bySeason := map[int][]*model.Item{}
	for _, it := range l.items {
		if it.Parsed.Type == model.TypeEpisode && Slug(it.Parsed.Show) == show.Key && needsLookup(refresh, it.TriedAt, it.Meta != nil) {
			bySeason[it.Parsed.Season] = append(bySeason[it.Parsed.Season], it)
		}
	}
	tmdbID := show.Meta.TmdbID
	l.mu.RUnlock()

	for season, eps := range bySeason {
		info, err := l.meta.LookupSeason(ctx, tmdbID, season)
		if err != nil {
			// Leave the episodes untried so the next scan retries the season.
			slog.Warn("season metadata failed", "show", show.Title, "season", season, "err", err)
			continue
		}
		for _, it := range eps {
			e, ok := info[it.Parsed.Episode]
			var gotStill bool
			if ok {
				p := l.artPath(it.ID, "backdrop")
				gotStill, err = l.meta.DownloadImage(ctx, e.StillPath, "w780", p)
				l.invalidateArt(p)
				if err != nil {
					slog.Warn("episode still failed", "show", show.Title, "season", season, "episode", it.Parsed.Episode, "err", err)
					continue
				}
			}
			l.mu.Lock()
			it.TriedAt = time.Now().UnixMilli()
			if ok {
				it.Meta = &model.Meta{Source: "tmdb", Title: e.Title, Overview: e.Overview, Runtime: e.Runtime, Rating: e.Rating}
				if gotStill {
					it.Art.Backdrop, it.Art.Thumb = true, false
				}
			}
			l.mu.Unlock()
		}
	}
}

// makeThumbnails gives anything still without artwork a frame from the video.
func (l *Library) makeThumbnails() {
	l.mu.RLock()
	var need []*model.Item
	for _, it := range l.items {
		if it.Info != nil && it.Info.Video != nil && !it.Art.Backdrop {
			if _, err := os.Stat(l.artPath(it.ID, "backdrop")); err != nil {
				need = append(need, it)
			}
		}
	}
	l.mu.RUnlock()
	l.setStatus(func(s *model.ScanStatus) {
		*s = model.ScanStatus{Scanning: true, Phase: "Creating thumbnails", Total: len(need)}
	})

	mapLimit(need, 2, func(it *model.Item) {
		defer l.bump()
		at := math.Max(1, math.Min(it.Info.Duration*0.1, 600))
		p := l.artPath(it.ID, "backdrop")
		err := runFFmpeg(time.Minute, "-v", "error", "-y", "-ss", fmt.Sprint(at), "-i", it.Path, "-frames:v", "1", "-vf", "scale=1280:-2", "-q:v", "4", p)
		l.invalidateArt(p)
		if err != nil {
			slog.Warn("thumbnail failed", "file", it.Rel, "err", err)
			return
		}
		l.mu.Lock()
		it.Art.Backdrop, it.Art.Thumb = true, true
		l.mu.Unlock()
	})
}

// SetProgress records a playback position. Positions past 92% (or within 120 s
// of the end) mark the item watched.
func (l *Library) SetProgress(id string, pos, dur float64) {
	l.mu.Lock()
	it, ok := l.items[id]
	if !ok {
		l.mu.Unlock()
		return
	}
	if dur == 0 && it.Info != nil {
		dur = it.Info.Duration
	}
	now := time.Now().UnixMilli()
	if dur > 0 && (pos/dur > 0.92 || dur-pos < 120) {
		l.progress[id] = model.Progress{Pos: 0, Dur: dur, Watched: true, T: now}
	} else {
		l.progress[id] = model.Progress{Pos: int(pos), Dur: dur, T: now}
	}
	l.mu.Unlock()
	l.stateSoon.Trigger()
}

// Progress returns the saved watch state of an item, if any.
func (l *Library) Progress(id string) (model.Progress, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	p, ok := l.progress[id]
	return p, ok
}

// MarkWatched sets or clears the watched flag.
func (l *Library) MarkWatched(id string, watched bool) {
	l.mu.Lock()
	it, ok := l.items[id]
	if !ok {
		l.mu.Unlock()
		return
	}
	if watched {
		var dur float64
		if it.Info != nil {
			dur = it.Info.Duration
		}
		l.progress[id] = model.Progress{Dur: dur, Watched: true, T: time.Now().UnixMilli()}
	} else {
		delete(l.progress, id)
	}
	l.mu.Unlock()
	l.stateSoon.Trigger()
}

// artVersion returns the cache-busting mtime of an art file, stat'ed once and
// then served from artVer until the library rewrites the file. Holding artMu
// across the stat means an invalidation cannot slip in between stat and store.
func (l *Library) artVersion(path string) int64 {
	l.artMu.Lock()
	defer l.artMu.Unlock()
	if v, ok := l.artVer[path]; ok {
		return v
	}
	var v int64
	if st, err := os.Stat(path); err == nil {
		v = st.ModTime().Unix()
	}
	l.artVer[path] = v
	return v
}

func (l *Library) artURL(key, kind string, has bool) *string {
	if !has {
		return nil
	}
	u := fmt.Sprintf("/art/%s/%s?v=%d", key, kind, l.artVersion(l.artPath(key, kind)))
	return &u
}

func (l *Library) publicItem(it *model.Item) model.PublicItem {
	meta := it.Meta
	if meta == nil {
		meta = &model.Meta{}
	}
	p := it.Parsed
	pi := model.PublicItem{
		ID: it.ID, Type: p.Type, Overview: meta.Overview, Tagline: meta.Tagline, Rating: meta.Rating,
		Genres: orEmpty(meta.Genres), Cast: orEmptyCast(meta.Cast), Duration: it.Info.Duration,
		Added: it.Added, Size: it.Size, File: filepath.Base(it.Path),
		Poster: l.artURL(it.ID, "poster", it.Art.Poster), Backdrop: l.artURL(it.ID, "backdrop", it.Art.Backdrop),
		Video: it.Info.Video, Audio: it.Info.Audio, PlayMode: "transcode", Subs: []model.PublicSub{},
	}
	if it.DirectPlay {
		pi.PlayMode = "direct"
	}
	if p.Type == model.TypeEpisode {
		pi.Title = meta.Title
		if pi.Title == "" {
			pi.Title = fmt.Sprintf("Episode %d", p.Episode)
		}
		season, episode := p.Season, p.Episode
		pi.Season, pi.Episode = &season, &episode
		if s := l.shows[Slug(p.Show)]; s != nil {
			title := s.Title
			if s.Meta != nil && s.Meta.Title != "" {
				title = s.Meta.Title
			}
			pi.Show = &model.ShowRef{Key: s.Key, Title: title}
		}
	} else {
		pi.Title = meta.Title
		if pi.Title == "" {
			pi.Title = p.Title
		}
	}
	pi.Year = meta.Year
	if pi.Year == nil {
		pi.Year = p.Year
	}
	pi.Runtime = meta.Runtime
	if pi.Runtime == 0 {
		pi.Runtime = int(math.Round(it.Info.Duration / 60))
	}
	for _, s := range it.Subs {
		pi.Subs = append(pi.Subs, model.PublicSub{ID: s.ID, Label: s.Label, Lang: s.Lang, Source: s.Source, Forced: s.Forced})
	}
	if pr, ok := l.progress[it.ID]; ok {
		pi.Progress = &model.PublicProgress{Pos: pr.Pos, Watched: pr.Watched, T: pr.T}
	}
	return pi
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyCast(c []model.CastMember) []model.CastMember {
	if c == nil {
		return []model.CastMember{}
	}
	return c
}

// Snapshot builds the GET /api/library response.
func (l *Library) Snapshot() model.Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	items := []model.PublicItem{}
	for _, it := range l.items {
		if it.Info != nil && it.Info.Video != nil {
			items = append(items, l.publicItem(it))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Added > items[j].Added })

	// Group episodes by show key in one pass.
	epsByShow := map[string][]model.PublicItem{}
	for _, it := range items {
		if it.Show != nil {
			epsByShow[it.Show.Key] = append(epsByShow[it.Show.Key], it)
		}
	}

	shows := []model.PublicShow{}
	for _, s := range l.shows {
		eps := epsByShow[s.Key]
		if len(eps) == 0 {
			continue
		}
		sort.Slice(eps, func(i, j int) bool {
			if *eps[i].Season != *eps[j].Season {
				return *eps[i].Season < *eps[j].Season
			}
			return *eps[i].Episode < *eps[j].Episode
		})
		ps := model.PublicShow{Key: s.Key, Title: s.Title, Year: s.Year, Genres: []string{}, Cast: []model.CastMember{}, Episodes: []string{}}
		if m := s.Meta; m != nil {
			if m.Title != "" {
				ps.Title = m.Title
			}
			if m.Year != nil {
				ps.Year = m.Year
			}
			ps.Overview, ps.Rating, ps.Genres, ps.Cast = m.Overview, m.Rating, orEmpty(m.Genres), orEmptyCast(m.Cast)
		}
		ps.Poster = l.artURL("show-"+s.Key, "poster", s.Art.Poster)
		ps.Backdrop = l.artURL("show-"+s.Key, "backdrop", s.Art.Backdrop)
		for _, e := range eps {
			if ps.Poster == nil && e.Backdrop != nil {
				ps.Poster = e.Backdrop
			}
			if ps.Backdrop == nil && e.Backdrop != nil {
				ps.Backdrop = e.Backdrop
			}
			ps.Added = math.Max(ps.Added, e.Added)
			ps.Episodes = append(ps.Episodes, e.ID)
		}
		shows = append(shows, ps)
	}
	sort.Slice(shows, func(i, j int) bool { return shows[i].Title < shows[j].Title })
	return model.Snapshot{Items: items, Shows: shows, Status: l.status}
}
