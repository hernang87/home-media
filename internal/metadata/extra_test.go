package metadata

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
)

// tmdbJSON writes a canned JSON body.
func tmdbJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

// newFakeTMDB starts an httptest server and a Client pointed at it. The
// handler sees every request; nothing ever reaches the real TMDB.
func newFakeTMDB(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{APIKey: "0123456789abcdef0123456789abcdef", APIBase: srv.URL, ImgBase: srv.URL + "/t/p"}, srv
}

func intPtr(v int) *int { return &v }

func TestTMDBMovieMatchByYear(t *testing.T) {
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/movie":
			if r.URL.Query().Get("year") != "1999" || r.URL.Query().Get("query") != "The Matrix" {
				t.Errorf("unexpected search query: %s", r.URL.RawQuery)
			}
			tmdbJSON(w, `{"results":[
				{"id":1,"title":"Other","release_date":"1984-06-01"},
				{"id":2,"title":"The Matrix","release_date":"1999-03-31"}]}`)
		case "/movie/2":
			if r.URL.Query().Get("append_to_response") != "credits" {
				t.Errorf("details missing append_to_response=credits: %s", r.URL.RawQuery)
			}
			tmdbJSON(w, `{"id":2,"title":"The Matrix","release_date":"1999-03-31",
				"overview":"A hacker.","tagline":"Welcome.","vote_average":8.2345,
				"genres":[{"id":28,"name":"Action"},{"id":878,"name":"Science Fiction"}],
				"runtime":136,"poster_path":"/p.jpg","backdrop_path":"/b.jpg",
				"credits":{"cast":[
					{"name":"Keanu Reeves","character":"Neo","profile_path":"/k.jpg"},
					{"name":"Nobody","character":"Extra","profile_path":null}]}}`)
		default:
			http.NotFound(w, r)
		}
	})

	m, err := c.LookupMovie(context.Background(), "The Matrix", intPtr(1999))
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected a match")
	}
	if m.TmdbID != 2 || m.Title != "The Matrix" || m.Source != "tmdb" {
		t.Errorf("bad identity: %+v", m)
	}
	if m.Year == nil || *m.Year != 1999 {
		t.Errorf("year = %v, want 1999", m.Year)
	}
	if m.Rating == nil || *m.Rating != 8.2 {
		t.Errorf("rating = %v, want 8.2", m.Rating)
	}
	if len(m.Genres) != 2 || m.Genres[1] != "Science Fiction" {
		t.Errorf("genres = %v", m.Genres)
	}
	if m.Runtime != 136 || m.Tagline != "Welcome." || m.PosterPath != "/p.jpg" || m.BackdropPath != "/b.jpg" {
		t.Errorf("bad details: %+v", m)
	}
	if len(m.Cast) != 2 {
		t.Fatalf("cast len = %d", len(m.Cast))
	}
	// newFakeTMDB sets ImgBase to the fake server, so only the suffix is fixed.
	if m.Cast[0].Role != "Neo" || m.Cast[0].Photo == nil || !strings.HasSuffix(*m.Cast[0].Photo, "/w185/k.jpg") {
		t.Errorf("cast[0] = %+v", m.Cast[0])
	}
	if m.Cast[1].Photo != nil {
		t.Errorf("cast[1].Photo should be nil, got %q", *m.Cast[1].Photo)
	}
}

func TestTMDBMovieDefaultImageBase(t *testing.T) {
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/movie":
			tmdbJSON(w, `{"results":[{"id":2,"release_date":"1999-03-31"}]}`)
		case "/movie/2":
			tmdbJSON(w, `{"id":2,"title":"X","credits":{"cast":[{"name":"A","character":"B","profile_path":"/k.jpg"}]}}`)
		default:
			http.NotFound(w, r)
		}
	})
	c.ImgBase = ""
	m, err := c.LookupMovie(context.Background(), "X", nil)
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if got := *m.Cast[0].Photo; got != "https://image.tmdb.org/t/p/w185/k.jpg" {
		t.Errorf("photo = %q", got)
	}
	if m.Rating != nil {
		t.Errorf("rating should be nil when vote_average is 0")
	}
}

func TestTMDBMovieYearPlusMinusOne(t *testing.T) {
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/movie":
			tmdbJSON(w, `{"results":[
				{"id":1,"release_date":"2001-01-01"},
				{"id":2,"release_date":"1998-05-05"}]}`)
		case "/movie/2":
			tmdbJSON(w, `{"id":2,"title":"Close","release_date":"1998-05-05"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	m, err := c.LookupMovie(context.Background(), "Close", intPtr(1999))
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if m.TmdbID != 2 {
		t.Errorf("picked %d, want 2 (1998 is within one year of 1999)", m.TmdbID)
	}
}

func TestTMDBMovieFallbackWithoutYear(t *testing.T) {
	var mu sync.Mutex
	var searches []string
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/movie":
			mu.Lock()
			searches = append(searches, r.URL.Query().Get("year"))
			mu.Unlock()
			if r.URL.Query().Get("year") != "" {
				tmdbJSON(w, `{"results":[]}`)
				return
			}
			// Release year is far from the filename year; still picked as results[0].
			tmdbJSON(w, `{"results":[{"id":7,"release_date":"2010-01-01"}]}`)
		case "/movie/7":
			tmdbJSON(w, `{"id":7,"title":"Fallback","release_date":"2010-01-01"}`)
		default:
			http.NotFound(w, r)
		}
	})
	m, err := c.LookupMovie(context.Background(), "Fallback", intPtr(1999))
	if err != nil || m == nil {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if m.TmdbID != 7 {
		t.Errorf("tmdbId = %d, want 7", m.TmdbID)
	}
	if len(searches) != 2 || searches[0] != "1999" || searches[1] != "" {
		t.Errorf("search years = %q, want [1999 \"\"]", searches)
	}
}

func TestTMDBMovieNoResults(t *testing.T) {
	var searches int
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" {
			t.Errorf("no details request expected, got %s", r.URL.Path)
		}
		searches++
		tmdbJSON(w, `{"results":[]}`)
	})
	m, err := c.LookupMovie(context.Background(), "Nothing", intPtr(2000))
	if m != nil || err != nil {
		t.Fatalf("want nil, nil; got %+v, %v", m, err)
	}
	if searches != 2 {
		t.Errorf("searches = %d, want 2 (with and without year)", searches)
	}
}

func TestTMDBShowLookupAndSeason(t *testing.T) {
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/tv":
			if r.URL.Query().Get("first_air_date_year") != "2008" {
				t.Errorf("missing first_air_date_year: %s", r.URL.RawQuery)
			}
			tmdbJSON(w, `{"results":[{"id":42,"name":"Show","first_air_date":"2008-01-20"}]}`)
		case "/tv/42":
			tmdbJSON(w, `{"id":42,"name":"Show","first_air_date":"2008-01-20","vote_average":0}`)
		case "/tv/42/season/1":
			tmdbJSON(w, `{"episodes":[
				{"episode_number":1,"name":"Pilot","overview":"Start","runtime":58,"still_path":"/s1.jpg","vote_average":8.26},
				{"episode_number":2,"name":"Two","overview":"","runtime":0,"still_path":null,"vote_average":0}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	s, err := c.LookupShow(ctx, "Show", intPtr(2008))
	if err != nil || s == nil {
		t.Fatalf("show: %v, %v", s, err)
	}
	if s.TmdbID != 42 || s.Title != "Show" || s.Year == nil || *s.Year != 2008 {
		t.Errorf("bad show: %+v", s)
	}

	eps, err := c.LookupSeason(ctx, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("episodes = %d, want 2", len(eps))
	}
	e1 := eps[1]
	if e1.Title != "Pilot" || e1.Runtime != 58 || e1.StillPath != "/s1.jpg" {
		t.Errorf("ep1 = %+v", e1)
	}
	if e1.Rating == nil || *e1.Rating != 8.3 {
		t.Errorf("ep1 rating = %v, want 8.3", e1.Rating)
	}
	if eps[2].Rating != nil {
		t.Errorf("ep2 rating should be nil")
	}
}

func TestTMDBAuthV3AndV4(t *testing.T) {
	const v4 = "eyJhbGciOiJIUzI1NiJ9.test"
	cases := []struct {
		name       string
		key        string
		wantBearer string
		wantAPIKey string
	}{
		{"v3 uses api_key", "0123456789abcdef0123456789abcdef", "", "0123456789abcdef0123456789abcdef"},
		{"v4 uses bearer", v4, "Bearer " + v4, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.wantBearer {
					t.Errorf("Authorization = %q, want %q", got, tc.wantBearer)
				}
				q := r.URL.Query()
				if got := q.Get("api_key"); got != tc.wantAPIKey {
					t.Errorf("api_key = %q, want %q", got, tc.wantAPIKey)
				}
				if q.Get("language") != "en-US" {
					t.Errorf("language = %q, want en-US default", q.Get("language"))
				}
				tmdbJSON(w, `{"results":[]}`)
			})
			c.APIKey = tc.key
			if m, err := c.LookupMovie(context.Background(), "x", nil); m != nil || err != nil {
				t.Fatalf("m=%v err=%v", m, err)
			}
		})
	}
}

func TestTMDBErrorHasRouteAndStatus(t *testing.T) {
	c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})
	_, err := c.LookupMovie(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "/search/movie") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want route and 401", err)
	}
	if strings.Contains(err.Error(), c.APIKey) {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestTMDBDownloadImage(t *testing.T) {
	ctx := context.Background()
	t.Run("200 writes file", func(t *testing.T) {
		c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/t/p/w500/abc.jpg" {
				t.Errorf("path = %s", r.URL.Path)
			}
			io.WriteString(w, "JPEGDATA")
		})
		dest := filepath.Join(t.TempDir(), "poster.jpg")
		ok, err := c.DownloadImage(ctx, "/abc.jpg", "w500", dest)
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		b, err := os.ReadFile(dest)
		if err != nil || string(b) != "JPEGDATA" {
			t.Errorf("file = %q, %v", b, err)
		}
	})
	t.Run("404 returns false", func(t *testing.T) {
		c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
		dest := filepath.Join(t.TempDir(), "missing.jpg")
		ok, err := c.DownloadImage(ctx, "/nope.jpg", "w500", dest)
		if ok || err != nil {
			t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Errorf("dest should not exist after 404")
		}
	})
	t.Run("empty path skips request", func(t *testing.T) {
		var hits int
		c, _ := newFakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
		ok, err := c.DownloadImage(ctx, "", "w500", filepath.Join(t.TempDir(), "x.jpg"))
		if ok || err != nil {
			t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
		}
		if hits != 0 {
			t.Errorf("made %d requests for empty path", hits)
		}
	})
}
