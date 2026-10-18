package subs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"homemedia/internal/model"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := ffmpegBin(); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const cue = "1\n00:00:01,000 --> 00:00:02,500\nHello there\n"

func TestLoadVTTWindows1252SRT(t *testing.T) {
	// "café" and curly quotes: é is 0xE9, “ is 0x93, ” is 0x94 in windows-1252.
	raw := []byte("1\n00:00:01,000 --> 00:00:02,000\ncaf\xe9 \x93hi\x94\n")
	p := writeFile(t, "fr.srt", raw)
	got, err := LoadVTT("/unused.mkv", model.SubTrack{Source: "external", File: p})
	if err != nil {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\ncafé “hi”\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadVTTUTF8BOMAndCRLF(t *testing.T) {
	raw := append([]byte("\xef\xbb\xbf"), []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nCafé\r\n")...)
	p := writeFile(t, "bom.srt", raw)
	got, err := LoadVTT("/unused.mkv", model.SubTrack{Source: "external", File: p})
	if err != nil {
		t.Fatal(err)
	}
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nCafé\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "\uFEFF") || strings.Contains(got, "\r") {
		t.Errorf("BOM or CR survived: %q", got)
	}
}

func TestSRTToVTTStripsTagsAndFonts(t *testing.T) {
	in := "\uFEFF1\r\n00:00:01,000 --> 00:00:02,000\r\n{\\an8}<FONT color=red>Top</font>\r\n"
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nTop\n"
	if got := SRTToVTT(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadVTTVTTPassthrough(t *testing.T) {
	raw := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n"
	p := writeFile(t, "x.vtt", []byte(raw))
	got, err := LoadVTT("/unused.mkv", model.SubTrack{Source: "external", File: p})
	if err != nil {
		t.Fatal(err)
	}
	if got != raw {
		t.Errorf("got %q, want %q", got, raw)
	}
}

func TestLoadVTTASSViaFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	ass := `[Script Info]
ScriptType: v4.00+
PlayResX: 320
PlayResY: 240

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,ASS line here
`
	p := writeFile(t, "sub.ass", []byte(ass))
	got, err := LoadVTT("/unused.mkv", model.SubTrack{Source: "external", File: p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "WEBVTT") || !strings.Contains(got, "ASS line here") {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestLoadVTTEmbeddedViaFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	srt := filepath.Join(dir, "sub.srt")
	if err := os.WriteFile(srt, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "clip.mkv")
	bin, _ := ffmpegBin()
	cmd := exec.Command(bin, "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-i", srt,
		"-map", "0:v", "-map", "1:s",
		"-c:v", "mpeg4", "-c:s", "srt", "-t", "2", mkv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building fixture: %v: %s", err, out)
	}
	got, err := LoadVTT(mkv, model.SubTrack{Source: "embedded", Index: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "WEBVTT") || !strings.Contains(got, "Hello there") {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestDescribeSubFileEdgeCases(t *testing.T) {
	cases := map[string]model.SubDescription{
		"English.srt":         {Lang: "en"},
		"SDH.eng.HI.srt":      {Lang: "eng", SDH: true},
		"movie.es.forced.srt": {Lang: "es", Forced: true},
		".srt":                {},
		"ger.srt":             {Lang: "deu"},
	}
	for in, want := range cases {
		if got := DescribeSubFile(in); got != want {
			t.Errorf("%q: got %+v, want %+v", in, got, want)
		}
	}
}

func TestSubLabelDedup(t *testing.T) {
	got := SubLabel(model.SubDescription{Lang: "en", Title: "Forced", Forced: true})
	if got != "English (Forced)" {
		t.Errorf("got %q", got)
	}
}
