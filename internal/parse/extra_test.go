package parse

import (
	"path/filepath"
	"reflect"
	"testing"

	"homemedia/internal/model"
)

func intp(n int) *int { return &n }

func TestCleanTitleBranches(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		title string
		year  *int
	}{
		{"year after title cuts title", "The.Movie.2026.1080p.WEB-DL", "The Movie", intp(2026)},
		{"year at start is kept in title", "2012", "2012", intp(2012)},
		{"start year plus later year uses later", "1917.2019.1080p", "1917", intp(2019)},
		{"start year with quality tag", "2012.1080p.BluRay", "2012", intp(2012)},
		{"year glued to digit is rejected", "Movie.20195", "Movie 20195", nil},
		{"year followed by letter is rejected", "Movie.2019a", "Movie 2019a", nil},
		{"quality cut without year", "Some.Show.S01.720p.HDTV", "Some Show S01", nil},
		{"quality at start falls back to raw", "1080p.Film", "1080p Film", nil},
		{"x264 quality cut", "Film.x264", "Film", nil},
		{"bracket group dropped", "[Group] Movie Name (2019) [1080p]", "Movie Name", intp(2019)},
		{"bracket group without year", "Movie [Group] Name", "Movie Name", nil},
		{"paren non-year group dropped", "Movie (2019 HD) 1080p", "Movie", nil},
		{"empty result falls back to raw", ".", "", nil},
		{"dots and underscores become spaces", "Some_Film.Name", "Some Film Name", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, year := CleanTitle(c.in)
			if title != c.title || !reflect.DeepEqual(year, c.year) {
				t.Errorf("CleanTitle(%q) = (%q, %v), want (%q, %v)", c.in, title, year, c.title, c.year)
			}
		})
	}
}

func TestParseMediaPathBranches(t *testing.T) {
	ep := func(show string, showYear *int, s, e int) model.Parsed {
		return model.Parsed{Type: model.TypeEpisode, Show: show, ShowYear: showYear, Season: s, Episode: e}
	}
	mv := func(title string, year *int) model.Parsed {
		return model.Parsed{Type: model.TypeMovie, Title: title, Year: year}
	}
	cases := []struct {
		name string
		in   string
		want model.Parsed
	}{
		// Episode patterns.
		{"SxxEyy with show from file", "Show Name/Show.Name.S01E02.1080p.mkv", ep("Show Name", nil, 1, 2)},
		{"SxxEyy show from file, no dir", "Show.S01E01.mkv", ep("Show", nil, 1, 1)},
		{"multi-episode SxxEyyEzz keeps first", "Show/Show.S01E01E02.mkv", ep("Show", nil, 1, 1)},
		{"multi-episode with dash separator", "Show/Show.S01E01-E02.mkv", ep("Show", nil, 1, 1)},
		{"1x02 pattern, show from dir", "Show/1x02.mkv", ep("Show", nil, 1, 2)},
		{"1x02 with show in file name", "Show/Show - 1x02.mkv", ep("Show", nil, 1, 2)},
		{"SxxEyy with 3-digit episode", "Show/S02E100.mkv", ep("Show", nil, 2, 100)},
		{"lowercase s and e", "Show/show.s03e04.mkv", ep("show", nil, 3, 4)},

		// Season-directory skipping.
		{"season folder skipped for show name", "Breaking Bad/Season 03/S03E04.mkv", ep("Breaking Bad", nil, 3, 4)},
		{"S<n> folder skipped", "Show/S1/S01E01.mkv", ep("Show", nil, 1, 1)},
		{"temporada folder skipped", "Show/Temporada 2/1x03.mkv", ep("Show", nil, 1, 3)},
		{"nearest non-season dir wins", "Show/Season 1/Extras/S01E01.mkv", ep("Extras", nil, 1, 1)},
		{"only season dirs means base name", "Season 1/S01E01.mkv", ep("S01E01", nil, 1, 1)},
		{"show year from dir", "Show (2011)/Season 1/S01E01.mkv", ep("Show", intp(2011), 1, 1)},

		// Year at start of file name.
		{"movie named like a year", "2012.mkv", mv("2012", intp(2012))},

		// Movie: folder-year fallback.
		{"folder year used when file has none", "Movie (2010)/movie.mkv", mv("Movie", intp(2010))},
		{"file year wins over folder year", "Other (2010)/Film.2020.mkv", mv("Film", intp(2020))},
		{"folder without year ignored", "Some Folder/film.mkv", mv("film", nil)},
		{"plain movie with no dirs", "The.Film.mkv", mv("The Film", nil)},
		{"nested movie uses file year", "a/b/The.Film.2001.mkv", mv("The Film", intp(2001))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseMediaPath(filepath.FromSlash(c.in))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseMediaPath(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}
