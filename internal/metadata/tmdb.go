// Package metadata talks to TMDB and caches artwork.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"homemedia/internal/model"
)

const (
	defaultAPIBase  = "https://api.themoviedb.org/3"
	defaultImgBase  = "https://image.tmdb.org/t/p"
	defaultLanguage = "en-US"
	apiTimeout      = 15 * time.Second
	imgTimeout      = 30 * time.Second
	maxCast         = 10
)

// Client is a TMDB client. APIKey may be a v3 key (32 hex chars, sent as
// api_key) or a v4 read token (starts with "ey", sent as Bearer).
//
// APIKey and Language are read when the client is built; to change them while
// requests are in flight, use SetCredentials.
type Client struct {
	APIKey   string
	Language string       // e.g. "en-US"
	HTTP     *http.Client // nil = 15 s timeout default
	APIBase  string       // default https://api.themoviedb.org/3; overridable for tests
	ImgBase  string       // default https://image.tmdb.org/t/p

	mu sync.RWMutex // guards APIKey and Language after construction
}

// SetCredentials replaces the API key and language; it is safe to call while
// lookups are running. An empty language keeps the default.
func (c *Client) SetCredentials(apiKey, language string) {
	c.mu.Lock()
	c.APIKey, c.Language = apiKey, language
	c.mu.Unlock()
}

// Credentials returns the current API key and language ("" if unset).
func (c *Client) Credentials() (apiKey, language string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.APIKey, c.Language
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool {
	key, _ := c.Credentials()
	return key != ""
}

// Check asks TMDB whether apiKey is accepted, without touching c's own
// credentials. It returns an error that never contains the key.
func (c *Client) Check(ctx context.Context, apiKey string) error {
	probe := &Client{APIKey: apiKey, Language: defaultLanguage, HTTP: c.HTTP, APIBase: c.APIBase, ImgBase: c.ImgBase}
	var out struct{}
	return probe.getJSON(ctx, "/configuration", nil, &out)
}

// LookupMovie searches by title/year (retrying without year when empty),
// picks the best match by release year (+-1 allowed), then loads details and credits.
// Returns nil, nil when nothing matches.
func (c *Client) LookupMovie(ctx context.Context, title string, year *int) (*model.Meta, error) {
	y := yearValue(year)
	var found searchResponse
	if err := c.getJSON(ctx, "/search/movie", searchParams(title, y, "year"), &found); err != nil {
		return nil, err
	}
	// A wrong year in the filename shouldn't hide the movie.
	if len(found.Results) == 0 && y != 0 {
		var retry searchResponse
		if err := c.getJSON(ctx, "/search/movie", searchParams(title, 0, "year"), &retry); err != nil {
			return nil, err
		}
		found = retry
	}
	i := bestMatch(found.Results, y, releaseDate)
	if i < 0 {
		return nil, nil
	}
	var d movieDetail
	route := fmt.Sprintf("/movie/%d", found.Results[i].ID)
	if err := c.getJSON(ctx, route, url.Values{"append_to_response": {"credits"}}, &d); err != nil {
		return nil, err
	}
	img := c.imgBase()
	return &model.Meta{
		Source:       "tmdb",
		TmdbID:       d.ID,
		Title:        d.Title,
		Year:         yearPtr(d.ReleaseDate),
		Overview:     d.Overview,
		Tagline:      d.Tagline,
		Rating:       roundRating(d.VoteAverage),
		Genres:       genreNames(d.Genres),
		Runtime:      d.Runtime,
		PosterPath:   d.PosterPath,
		BackdropPath: d.BackdropPath,
		Cast:         toCast(d.Credits.Cast, img),
	}, nil
}

// LookupShow is LookupMovie for TV series.
func (c *Client) LookupShow(ctx context.Context, title string, year *int) (*model.Meta, error) {
	y := yearValue(year)
	var found searchResponse
	if err := c.getJSON(ctx, "/search/tv", searchParams(title, y, "first_air_date_year"), &found); err != nil {
		return nil, err
	}
	id := 0
	ok := false
	if i := bestMatch(found.Results, y, firstAirDate); i >= 0 {
		id, ok = found.Results[i].ID, true
	} else if y != 0 {
		// Same fallback as the Node port: retry without year, take the first hit.
		var retry searchResponse
		if err := c.getJSON(ctx, "/search/tv", searchParams(title, 0, "first_air_date_year"), &retry); err != nil {
			return nil, err
		}
		if len(retry.Results) > 0 {
			id, ok = retry.Results[0].ID, true
		}
	}
	if !ok {
		return nil, nil
	}
	var d tvDetail
	if err := c.getJSON(ctx, fmt.Sprintf("/tv/%d", id), url.Values{"append_to_response": {"credits"}}, &d); err != nil {
		return nil, err
	}
	return &model.Meta{
		Source:       "tmdb",
		TmdbID:       d.ID,
		Title:        d.Name,
		Year:         yearPtr(d.FirstAirDate),
		Overview:     d.Overview,
		Rating:       roundRating(d.VoteAverage),
		Genres:       genreNames(d.Genres),
		PosterPath:   d.PosterPath,
		BackdropPath: d.BackdropPath,
		Cast:         toCast(d.Credits.Cast, c.imgBase()),
	}, nil
}

// LookupSeason returns episode metadata keyed by episode number.
func (c *Client) LookupSeason(ctx context.Context, tmdbID, season int) (map[int]model.EpisodeMeta, error) {
	var d seasonDetail
	if err := c.getJSON(ctx, fmt.Sprintf("/tv/%d/season/%d", tmdbID, season), nil, &d); err != nil {
		return nil, err
	}
	out := make(map[int]model.EpisodeMeta, len(d.Episodes))
	for _, e := range d.Episodes {
		out[e.EpisodeNumber] = model.EpisodeMeta{
			Title:     e.Name,
			Overview:  e.Overview,
			Runtime:   e.Runtime,
			StillPath: e.StillPath,
			Rating:    roundRating(e.VoteAverage),
		}
	}
	return out, nil
}

// DownloadImage saves ImgBase/size+tmdbPath to dest. Returns false (no error)
// when tmdbPath is empty or the server answers non-200.
func (c *Client) DownloadImage(ctx context.Context, tmdbPath, size, dest string) (bool, error) {
	if tmdbPath == "" {
		return false, nil
	}
	target := c.imgBase() + "/" + size + tmdbPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, fmt.Errorf("TMDB image %s%s: %w", size, tmdbPath, scrubURL(err))
	}
	res, err := c.httpClient(imgTimeout).Do(req)
	if err != nil {
		return false, fmt.Errorf("TMDB image %s%s: %w", size, tmdbPath, scrubURL(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, nil
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return false, fmt.Errorf("TMDB image %s%s: read body: %w", size, tmdbPath, err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	return true, nil
}

// ---- request plumbing ----

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return defaultAPIBase
}

func (c *Client) imgBase() string {
	if c.ImgBase != "" {
		return strings.TrimRight(c.ImgBase, "/")
	}
	return defaultImgBase
}

func (c *Client) language() string {
	if _, lang := c.Credentials(); lang != "" {
		return lang
	}
	return defaultLanguage
}

func (c *Client) httpClient(d time.Duration) *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: d}
}

// getJSON performs an authenticated GET against the TMDB API and decodes the body into out.
// v4 read tokens (starting "ey") go in the Authorization header; v3 keys go in api_key.
func (c *Client) getJSON(ctx context.Context, route string, params url.Values, out any) error {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("language", c.language())
	headers := http.Header{"Accept": {"application/json"}}
	apiKey, _ := c.Credentials()
	if strings.HasPrefix(apiKey, "ey") {
		headers.Set("Authorization", "Bearer "+apiKey)
	} else {
		q.Set("api_key", apiKey)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+route+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("TMDB %s: %w", route, scrubURL(err))
	}
	req.Header = headers
	res, err := c.httpClient(apiTimeout).Do(req)
	if err != nil {
		return fmt.Errorf("TMDB %s: %w", route, scrubURL(err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("TMDB %s -> %d", route, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("TMDB %s: decode response: %w", route, err)
	}
	return nil
}

// scrubURL drops the request URL from url.Error so the API key in a query
// string never ends up in logs.
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ---- wire types (only the fields we use) ----

type searchResponse struct {
	Results []searchHit `json:"results"`
}

type searchHit struct {
	ID           int    `json:"id"`
	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`
}

type genre struct {
	Name string `json:"name"`
}

type castMember struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
}

type credits struct {
	Cast []castMember `json:"cast"`
}

type movieDetail struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	ReleaseDate  string  `json:"release_date"`
	Overview     string  `json:"overview"`
	Tagline      string  `json:"tagline"`
	VoteAverage  float64 `json:"vote_average"`
	Genres       []genre `json:"genres"`
	Runtime      int     `json:"runtime"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Credits      credits `json:"credits"`
}

type tvDetail struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	FirstAirDate string  `json:"first_air_date"`
	Overview     string  `json:"overview"`
	VoteAverage  float64 `json:"vote_average"`
	Genres       []genre `json:"genres"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Credits      credits `json:"credits"`
}

type episode struct {
	EpisodeNumber int     `json:"episode_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	Runtime       int     `json:"runtime"`
	StillPath     string  `json:"still_path"`
	VoteAverage   float64 `json:"vote_average"`
}

type seasonDetail struct {
	Episodes []episode `json:"episodes"`
}

// ---- helpers (ports of the JS helpers) ----

func searchParams(title string, year int, yearKey string) url.Values {
	p := url.Values{"query": {title}}
	if year != 0 {
		p.Set(yearKey, strconv.Itoa(year))
	}
	return p
}

func yearValue(year *int) int {
	if year == nil {
		return 0
	}
	return *year
}

// yearOf is the Node yearOf: the first four characters of a TMDB date, or 0 when absent.
func yearOf(d string) int {
	if len(d) < 4 {
		return 0
	}
	n, err := strconv.Atoi(d[:4])
	if err != nil {
		return 0
	}
	return n
}

func yearPtr(d string) *int {
	y := yearOf(d)
	if y == 0 {
		return nil
	}
	return &y
}

func releaseDate(h searchHit) string  { return h.ReleaseDate }
func firstAirDate(h searchHit) string { return h.FirstAirDate }

// bestMatch returns the index of the chosen hit, or -1 when there are no hits.
// Without a year the first hit wins; otherwise an exact year match, then one
// within +-1 year, then the first hit.
func bestMatch(hits []searchHit, year int, date func(searchHit) string) int {
	if len(hits) == 0 {
		return -1
	}
	if year == 0 {
		return 0
	}
	for i, h := range hits {
		if yearOf(date(h)) == year {
			return i
		}
	}
	for i, h := range hits {
		if d := yearOf(date(h)) - year; d >= -1 && d <= 1 {
			return i
		}
	}
	return 0
}

// roundRating rounds to one decimal; a zero vote average means "no rating".
func roundRating(v float64) *float64 {
	if v == 0 {
		return nil
	}
	r := math.Round(v*10) / 10
	return &r
}

func genreNames(gs []genre) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}

func toCast(cs []castMember, imgBase string) []model.CastMember {
	n := len(cs)
	if n > maxCast {
		n = maxCast
	}
	out := make([]model.CastMember, 0, n)
	for _, c := range cs[:n] {
		m := model.CastMember{Name: c.Name, Role: c.Character}
		if c.ProfilePath != "" {
			p := imgBase + "/w185" + c.ProfilePath
			m.Photo = &p
		}
		out = append(out, m)
	}
	return out
}
