package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homemedia/internal/model"
)

func TestPlaylist(t *testing.T) {
	got := Playlist(14)
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:6.000,\nseg00000.ts\n#EXTINF:6.000,\nseg00001.ts\n#EXTINF:2.000,\nseg00002.ts\n#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if SegmentCount(0) != 1 || SegmentCount(12) != 2 || SegmentCount(12.1) != 3 {
		t.Error("SegmentCount")
	}
	if !strings.Contains(Playlist(0), "#EXTINF:0.001,") {
		t.Error("zero duration must still yield a valid segment")
	}
}

func fixture(t *testing.T) model.Item {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg missing")
	}
	p, _ := filepath.Abs("../../testdata/fixtures/Movies/Direct.Movie.2020.1080p/Direct.Movie.2020.1080p.mp4")
	if _, err := os.Stat(p); err != nil {
		t.Skip("run testdata/gen.sh first")
	}
	return model.Item{ID: "fx", Path: p, Rel: "fx.mp4"}
}

func TestSegmentsAndSeek(t *testing.T) {
	item := fixture(t)
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Jump straight to segment 2 (a seek), then go back to 0 (restart).
	for _, n := range []int{2, 0, 1} {
		f, err := m.Segment(ctx, item, 0, n)
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", f).Output()
		if err != nil || !strings.Contains(string(out), "video") {
			t.Fatalf("segment %d not playable: %v %s", n, err, out)
		}
	}
}

func TestSegmentHonoursContext(t *testing.T) {
	item := fixture(t)
	m, _ := NewManager(t.TempDir())
	defer m.StopAll()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Segment(ctx, item, 0, 3); err == nil {
		t.Error("cancelled context must return an error")
	}
}
