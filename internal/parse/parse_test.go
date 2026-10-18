package parse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"homemedia/internal/model"
)

type golden struct {
	ParseMediaPath []struct {
		In  string       `json:"in"`
		Out model.Parsed `json:"out"`
	} `json:"parseMediaPath"`
	CleanTitle []struct {
		In  string `json:"in"`
		Out struct {
			Title string `json:"title"`
			Year  *int   `json:"year"`
		} `json:"out"`
	} `json:"cleanTitle"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("../../testdata/golden/node.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestParseMediaPathGolden(t *testing.T) {
	for _, c := range loadGolden(t).ParseMediaPath {
		got := ParseMediaPath(filepath.FromSlash(c.In))
		if !reflect.DeepEqual(got, c.Out) {
			t.Errorf("%q: got %+v, want %+v", c.In, got, c.Out)
		}
	}
}

func TestCleanTitleGolden(t *testing.T) {
	for _, c := range loadGolden(t).CleanTitle {
		title, year := CleanTitle(c.In)
		if title != c.Out.Title || !reflect.DeepEqual(year, c.Out.Year) {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", c.In, title, year, c.Out.Title, c.Out.Year)
		}
	}
}
