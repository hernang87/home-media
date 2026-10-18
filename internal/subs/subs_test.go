package subs

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"homemedia/internal/model"
)

type golden struct {
	DescribeSubFile []struct {
		In  string               `json:"in"`
		Out model.SubDescription `json:"out"`
	} `json:"describeSubFile"`
	SubLabel []struct {
		In  model.SubDescription `json:"in"`
		Out string               `json:"out"`
	} `json:"subLabel"`
	LanguageName []struct{ In, Out string } `json:"languageName"`
	SrtToVtt     []struct{ In, Out string } `json:"srtToVtt"`
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

func TestDescribeSubFileGolden(t *testing.T) {
	for _, c := range loadGolden(t).DescribeSubFile {
		if got := DescribeSubFile(c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("%q: got %+v, want %+v", c.In, got, c.Out)
		}
	}
}

func TestSubLabelGolden(t *testing.T) {
	for _, c := range loadGolden(t).SubLabel {
		if got := SubLabel(c.In); got != c.Out {
			t.Errorf("%+v: got %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestLanguageNameGolden(t *testing.T) {
	for _, c := range loadGolden(t).LanguageName {
		if got := LanguageName(c.In); got != c.Out {
			t.Errorf("%q: got %q, want %q", c.In, got, c.Out)
		}
	}
}

func TestSRTToVTTGolden(t *testing.T) {
	for _, c := range loadGolden(t).SrtToVtt {
		if got := SRTToVTT(c.In); got != c.Out {
			t.Errorf("got %q, want %q", got, c.Out)
		}
	}
}
