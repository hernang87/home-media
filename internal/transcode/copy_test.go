package transcode

import (
	"bufio"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homemedia/internal/model"
)

func TestParseKeyframes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []float64
	}{
		{"keyframes only", "packet,0.000000,K__\npacket,0.042000,___\npacket,2.002000,K__\nformat,0.000000\n", []float64{0, 2.002}},
		{"start time is subtracted", "packet,10.000000,K__\npacket,12.500000,K__\nformat,10.000000\n", []float64{0, 2.5}},
		{"first keyframe moves to 0", "packet,0.083000,K__\npacket,2.083000,K__\nformat,0.000000\n", []float64{0, 2.083}},
		{"unordered and repeated", "packet,4.000000,K__\npacket,0.000000,K__\npacket,4.000000,K__\n", []float64{0, 4}},
		{"missing timestamps are skipped", "packet,N/A,K__\npacket,0.000000,K__\nformat,N/A\n", []float64{0}},
		{"no keyframes", "packet,0.000000,___\nformat,0.000000\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKeyframes(bufio.NewScanner(strings.NewReader(tt.in)))
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestUsableKeyframes(t *testing.T) {
	tests := []struct {
		name     string
		starts   []float64
		duration float64
		want     bool
	}{
		{"regular", []float64{0, 2, 4, 6}, 8, true},
		{"none", nil, 8, false},
		{"unknown duration", []float64{0, 2}, 0, false},
		{"gap too long", []float64{0, 2, 30}, 32, false},
		{"tail too long", []float64{0, 2}, 40, false},
		{"all intra", []float64{0, 0.04, 0.08, 0.12, 0.16, 0.2}, 0.24, false},
	}
	for _, tt := range tests {
		if got := usableKeyframes(tt.starts, tt.duration); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCopyPlaylist(t *testing.T) {
	got := copyPlaylist([]float64{0, 2.002, 9.5}, 12)
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:8\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:2.002,\nseg00000.ts\n#EXTINF:7.498,\nseg00001.ts\n#EXTINF:2.500,\nseg00002.ts\n#EXT-X-ENDLIST\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSessionWindows(t *testing.T) {
	encode := &session{}
	// A copy stream with a keyframe every 2 s.
	copyS := &session{starts: make([]float64, 500)}
	for i := range copyS.starts {
		copyS.starts[i] = float64(2 * i)
	}
	tests := []struct {
		name          string
		s             *session
		produced, req int
		wantAhead     bool
		wantKeepFrom  int
	}{
		{"encode at the limit", encode, 130, 100, false, 80},
		{"encode past the limit", encode, 131, 100, true, 80},
		{"encode near the start", encode, 5, 3, false, 0},
		{"encode nothing produced", encode, -1, 0, false, 0},
		{"copy at the limit", copyS, 190, 100, false, 40},
		{"copy past the limit", copyS, 191, 100, true, 40},
		{"copy near the start", copyS, 20, 10, false, 0},
	}
	for _, tt := range tests {
		if got := tt.s.tooFarAhead(tt.produced, tt.req); got != tt.wantAhead {
			t.Errorf("%s: tooFarAhead = %v, want %v", tt.name, got, tt.wantAhead)
		}
		if got := tt.s.keepFrom(tt.req); got != tt.wantKeepFrom {
			t.Errorf("%s: keepFrom = %d, want %d", tt.name, got, tt.wantKeepFrom)
		}
	}
	if got := copyS.startOf(9999); got != 998 {
		t.Errorf("startOf past the end = %v, want the last segment's start", got)
	}
}

func h264Item(id, path string) model.Item {
	return model.Item{ID: id, Path: path, Rel: filepath.Base(path), Size: 1, MTime: 1,
		Info: &model.MediaInfo{Duration: 20, Video: &model.VideoInfo{Codec: "h264"}}}
}

func TestKeyframeIndexIsCached(t *testing.T) {
	dir := t.TempDir()
	var scans atomic.Int32
	var fail atomic.Bool
	newManager := func() *Manager {
		m, err := NewManager(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.StopAll)
		m.IndexDir = filepath.Join(dir, "keys")
		m.probeKeys = func(context.Context, string) ([]float64, error) {
			scans.Add(1)
			if fail.Load() {
				return nil, errors.New("boom")
			}
			return []float64{0, 10}, nil
		}
		return m
	}
	ctx := context.Background()
	item := h264Item("a", "a.mkv")

	m := newManager()
	fail.Store(true)
	if m.CanCopy(ctx, item) {
		t.Error("a failed scan must not allow copy")
	}
	fail.Store(false)
	if !m.CanCopy(ctx, item) || !m.CanCopy(ctx, item) {
		t.Fatal("want copy after a good scan")
	}
	if got := scans.Load(); got != 2 {
		t.Errorf("scans = %d, want 2 (the failure is retried, the result is kept)", got)
	}
	if !newManager().CanCopy(ctx, item) || scans.Load() != 2 {
		t.Errorf("a new manager must read the index from disk; scans = %d", scans.Load())
	}

	changed := item
	changed.MTime = 2
	if !m.CanCopy(ctx, changed) || scans.Load() != 3 {
		t.Errorf("a changed file must be scanned again; scans = %d", scans.Load())
	}

	hevc := h264Item("b", "b.mkv")
	hevc.Info.Video.Codec = "hevc"
	if m.CanCopy(ctx, hevc) || scans.Load() != 3 {
		t.Error("only h264 is copied, and other codecs are not scanned")
	}

	// 20 s with one keyframe: unusable, and remembered as such.
	sparse := h264Item("c", "c.mkv")
	sparse.Info.Duration = 100
	if m.CanCopy(ctx, sparse) || m.CanCopy(ctx, sparse) || scans.Load() != 4 {
		t.Errorf("an unusable index must be remembered; scans = %d", scans.Load())
	}
	if _, ok := m.CopyPlaylist(ctx, sparse); ok {
		t.Error("no playlist without a copy stream")
	}
	if _, err := m.CopySegment(ctx, sparse, 0, 0); !errors.Is(err, ErrNoSegment) {
		t.Errorf("CopySegment = %v, want ErrNoSegment", err)
	}
}

func TestCanCopyGivesUpWithContext(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	defer m.StopAll()
	release := make(chan struct{})
	m.probeKeys = func(context.Context, string) ([]float64, error) {
		<-release
		return []float64{0, 10}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	item := h264Item("a", "a.mkv")
	if m.CanCopy(ctx, item) {
		t.Error("want false while the scan is still running")
	}
	close(release)
	// The scan went on in the background, so the next play can copy.
	if !m.CanCopy(context.Background(), item) {
		t.Error("want copy once the scan has finished")
	}
}

// videoPackets returns the pts (seconds) of each video packet in a segment
// file and how many of them are keyframes.
func videoPackets(t *testing.T, file string) (pts []float64, keys int) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 2 {
			continue
		}
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			pts = append(pts, v)
		}
		if strings.HasPrefix(f[1], "K") {
			keys++
		}
	}
	if len(pts) == 0 {
		t.Fatalf("%s has no video", file)
	}
	return pts, keys
}

// checkCopySegments asks for the given segments in order (so each jump is a
// seek) and checks that every one holds exactly its own keyframe interval.
func checkCopySegments(t *testing.T, m *Manager, item model.Item, order []int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	starts := m.keyframes(ctx, item)
	if starts == nil {
		t.Fatal("item has no copy stream")
	}
	// MPEG-TS output starts 1.4 s in (ffmpeg's default mux delay), plus tsBias.
	const tsDelay, tolerance = 1.4 + tsBias, 0.05
	for _, n := range order {
		if n < 0 {
			n += len(starts)
		}
		f, err := m.CopySegment(ctx, item, 0, n)
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		pts, keys := videoPackets(t, f)
		if keys != 1 {
			t.Errorf("segment %d has %d keyframes, want 1", n, keys)
		}
		if got := pts[0] - tsDelay; math.Abs(got-starts[n]) > tolerance {
			t.Errorf("segment %d starts at %.3f, want %.3f", n, got, starts[n])
		}
		if n+1 < len(starts) {
			if last := slices.Max(pts) - tsDelay; last >= starts[n+1] {
				t.Errorf("segment %d runs to %.3f, past the next keyframe at %.3f", n, last, starts[n+1])
			}
		}
	}
	if _, err := m.CopySegment(ctx, item, 0, len(starts)); !errors.Is(err, ErrNoSegment) {
		t.Errorf("segment past the end: %v, want ErrNoSegment", err)
	}
}

func TestCopySegmentsAndSeek(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg missing")
	}
	p, _ := filepath.Abs("../../testdata/fixtures/Movies/Multi.Audio.2018/Multi.Audio.2018.mkv")
	st, err := os.Stat(p)
	if err != nil {
		t.Skip("run testdata/gen.sh first")
	}
	item := model.Item{ID: "fx", Path: p, Rel: "fx.mkv", Size: st.Size(), MTime: 1,
		Info: &model.MediaInfo{Duration: 20, Video: &model.VideoInfo{Codec: "h264"}}}
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	checkCopySegments(t, m, item, []int{1, 0})

	playlist, ok := m.CopyPlaylist(context.Background(), item)
	if !ok || strings.Count(playlist, "#EXTINF") != 2 {
		t.Errorf("playlist = %q, %v; want 2 segments", playlist, ok)
	}
}

// TestCopyRealFile checks copy mode against a file of yours, which the small
// fixtures cannot stand in for (B-frames, thousands of keyframes):
//
//	HOMEMEDIA_COPY_FILE=/path/to/movie.mkv go test -run TestCopyRealFile ./internal/transcode
func TestCopyRealFile(t *testing.T) {
	p := os.Getenv("HOMEMEDIA_COPY_FILE")
	if p == "" {
		t.Skip("set HOMEMEDIA_COPY_FILE to run")
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	duration, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	item := model.Item{ID: "real", Path: p, Rel: filepath.Base(p), Size: st.Size(), MTime: 1,
		Info: &model.MediaInfo{Duration: duration, Video: &model.VideoInfo{Codec: "h264"}}}
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	n := len(m.keyframes(context.Background(), item))
	t.Logf("%d segments", n)
	// Start, continue, seek far ahead, continue, step back, jump to the end.
	checkCopySegments(t, m, item, []int{0, 1, 2, n / 2, n/2 + 1, n/2 - 1, -1})
}
