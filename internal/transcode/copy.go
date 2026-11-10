package transcode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"homemedia/internal/model"
)

// Copy mode ("remux") sends the file's own H.264 video in the HLS stream and
// only re-encodes the audio. Copied video can only be cut at its keyframes, so
// a copy stream has one segment per keyframe interval, and the playlist is
// built from the file's keyframe times instead of fixed SegSeconds steps.

const (
	// maxKeyGap is the longest keyframe interval (seconds) a copy stream
	// accepts; a longer one would make a segment too big to seek and buffer well.
	maxKeyGap = 20
	// minKeyGap is the shortest average keyframe interval (seconds) a copy
	// stream accepts; below it (all-intra video) the playlist would be huge.
	minKeyGap = 0.5
	// indexTimeout bounds one keyframe scan (it reads the whole file).
	indexTimeout = 5 * time.Minute
	// copyRate and copyBurst throttle ffmpeg in copy mode, which would
	// otherwise fill the temp dir at disk speed: copyBurst seconds of video are
	// produced at once, the rest at copyRate times playback speed.
	copyRate  = 3
	copyBurst = 60
	// seekSlack is how far before a keyframe (seconds) ffmpeg is told to start,
	// so rounding cannot make it skip that keyframe.
	seekSlack = 0.001
	// tsBias (seconds) is added to every timestamp of a copy stream.
	tsBias = 1
)

// ErrNoSegment means the requested segment is not part of the stream.
var ErrNoSegment = errors.New("no such segment")

// keyEntry is the keyframe index of one version (size and mtime) of a file.
// starts is nil when the file cannot be played in copy mode.
type keyEntry struct {
	Size   int64     `json:"size"`
	MTime  float64   `json:"mtime"`
	Starts []float64 `json:"starts"`

	done chan struct{} // closed when Starts is final
}

// copyable reports whether item's video can be copied into an MPEG-TS HLS
// stream at all. Whether a given TV can decode it is the caller's question.
func copyable(item model.Item) bool {
	return item.Info != nil && item.Info.Video != nil && item.Info.Video.Codec == "h264" && item.Info.Duration > 0
}

// parseKeyframes reads `ffprobe -show_entries packet=pts_time,flags:format=start_time
// -of csv=p=1` output and returns the keyframe times in seconds from the
// start of the file, ascending. The first is moved to 0: the first segment
// starts where the file does.
func parseKeyframes(out *bufio.Scanner) ([]float64, error) {
	var keys []float64
	start := 0.0
	for out.Scan() {
		f := strings.Split(strings.TrimSpace(out.Text()), ",")
		switch {
		case len(f) >= 3 && f[0] == "packet" && strings.HasPrefix(f[2], "K"):
			if t, err := strconv.ParseFloat(f[1], 64); err == nil {
				keys = append(keys, t)
			}
		case len(f) >= 2 && f[0] == "format":
			if t, err := strconv.ParseFloat(f[1], 64); err == nil {
				start = t
			}
		}
	}
	if err := out.Err(); err != nil {
		return nil, err
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	for i := range keys {
		keys[i] = math.Max(0, keys[i]-start)
	}
	if len(keys) > 0 {
		keys[0] = 0
	}
	return slices.Compact(keys), nil
}

// probeKeyframes lists the keyframe times of the first video stream of file.
func probeKeyframes(ctx context.Context, file string) ([]float64, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags:format=start_time", "-of", "csv=p=1", file)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	keys, err := parseKeyframes(bufio.NewScanner(stdout))
	if werr := cmd.Wait(); werr != nil {
		return nil, fmt.Errorf("ffprobe keyframes: %w", werr)
	}
	return keys, err
}

// usableKeyframes reports whether segments cut at starts are a sensible stream
// for a file of this duration (seconds).
func usableKeyframes(starts []float64, duration float64) bool {
	if len(starts) == 0 || duration <= 0 || float64(len(starts)) > duration/minKeyGap+1 {
		return false
	}
	for i, t := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if end-t > maxKeyGap {
			return false
		}
	}
	return true
}

func (m *Manager) keyPath(id string) string {
	return filepath.Join(m.IndexDir, id+".json")
}

// keyframes returns the copy-mode segment start times of item, or nil when it
// cannot be played in copy mode (or ctx ends first). The first call for a file
// scans it; the result is kept in memory and, with IndexDir set, on disk.
func (m *Manager) keyframes(ctx context.Context, item model.Item) []float64 {
	if !copyable(item) {
		return nil
	}
	m.mu.Lock()
	e := m.keys[item.ID]
	if e == nil || e.Size != item.Size || e.MTime != item.MTime {
		e = &keyEntry{Size: item.Size, MTime: item.MTime, done: make(chan struct{})}
		m.keys[item.ID] = e
		go m.loadKeys(item, e)
	}
	m.mu.Unlock()
	select {
	case <-e.done:
		return e.Starts
	case <-ctx.Done():
		return nil
	}
}

// loadKeys fills e from the disk cache or by scanning the file, then closes e.done.
func (m *Manager) loadKeys(item model.Item, e *keyEntry) {
	defer close(e.done)
	if m.IndexDir != "" {
		var saved keyEntry
		if b, err := os.ReadFile(m.keyPath(item.ID)); err == nil && json.Unmarshal(b, &saved) == nil &&
			saved.Size == e.Size && saved.MTime == e.MTime {
			e.Starts = saved.Starts
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), indexTimeout)
	defer cancel()
	began := time.Now()
	starts, err := m.probeKeys(ctx, item.Path)
	if err != nil {
		// Not remembered: the next play tries again.
		slog.Warn("keyframe scan failed", "file", item.Rel, "err", err)
		m.mu.Lock()
		if m.keys[item.ID] == e {
			delete(m.keys, item.ID)
		}
		m.mu.Unlock()
		return
	}
	if usableKeyframes(starts, item.Info.Duration) {
		e.Starts = starts
	}
	slog.Info("keyframes indexed", "file", item.Rel, "keyframes", len(starts), "copy", e.Starts != nil, "took", time.Since(began).Round(time.Millisecond))
	if m.IndexDir == "" {
		return
	}
	b, err := json.Marshal(e)
	if err == nil {
		if err = os.MkdirAll(m.IndexDir, 0o755); err == nil {
			tmp := m.keyPath(item.ID) + ".tmp"
			if err = os.WriteFile(tmp, b, 0o644); err == nil {
				err = os.Rename(tmp, m.keyPath(item.ID))
			}
		}
	}
	if err != nil {
		slog.Warn("keyframe index not saved", "file", item.Rel, "err", err)
	}
}

// CanCopy reports whether item can be streamed in copy mode. It waits for the
// keyframe scan of a file seen for the first time, until ctx ends; the scan
// carries on in the background so the next play finds it done.
func (m *Manager) CanCopy(ctx context.Context, item model.Item) bool {
	return m.keyframes(ctx, item) != nil
}

// CopyPlaylist returns the complete VOD playlist of item's copy stream, or
// false when the item has none.
func (m *Manager) CopyPlaylist(ctx context.Context, item model.Item) (string, bool) {
	starts := m.keyframes(ctx, item)
	if starts == nil {
		return "", false
	}
	return copyPlaylist(starts, item.Info.Duration), true
}

// copyPlaylist is CopyPlaylist for known segment start times.
func copyPlaylist(starts []float64, duration float64) string {
	lengths := make([]float64, len(starts))
	longest := 0.0
	for i, t := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		lengths[i] = math.Max(end-t, 0.001)
		longest = math.Max(longest, lengths[i])
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", int(math.Ceil(longest)))
	for i, length := range lengths {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", length, SegmentName(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// CopySegment is Segment for item's copy stream. It returns ErrNoSegment when
// the item has no copy stream or n is past its end.
func (m *Manager) CopySegment(ctx context.Context, item model.Item, audio, n int) (string, error) {
	starts := m.keyframes(ctx, item)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if n < 0 || n >= len(starts) {
		return "", ErrNoSegment
	}
	s, err := m.session(item, audio, starts)
	if err != nil {
		return "", err
	}
	return m.segment(ctx, s, n)
}

// copyArgs are the ffmpeg arguments that write copy-mode segments from
// segment seg on, which starts at the keyframe at start seconds.
func copyArgs(item model.Item, audio, seg int, start float64, dir string) []string {
	// ffmpeg seeks to a keyframe at or before -ss (often one too early); with
	// -copypriorss 0 the copy then begins at the first keyframe from -ss on,
	// which is exactly the one segment seg starts with.
	ss := strconv.FormatFloat(math.Max(0, start-seekSlack), 'f', 6, 64)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	args = append(args, "-readrate", strconv.Itoa(copyRate), "-readrate_initial_burst", strconv.Itoa(copyBurst))
	args = append(args, "-ss", ss, "-i", item.Path, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-sn", "-dn")
	args = append(args, "-c:v", "copy", "-copypriorss", "0")
	args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k", "-af", "aresample=async=1:first_pts=0")
	// Keep the source timestamps (plus a constant) whatever segment ffmpeg
	// starts at, so segments from different runs line up. tsBias keeps the
	// first decode timestamps, which precede the first frame when the video
	// has B-frames, above zero; the muxer would shift a run that starts below.
	offset := strconv.FormatFloat(math.Max(0, start-seekSlack)+tsBias, 'f', 6, 64)
	args = append(args, "-output_ts_offset", offset)
	// A 1 µs target length makes the muxer cut at every keyframe.
	args = append(args, "-f", "hls", "-hls_time", "0.000001", "-hls_list_size", "0", "-hls_flags", "temp_file")
	args = append(args, "-start_number", strconv.Itoa(seg), "-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"), filepath.Join(dir, fmt.Sprintf("ff%d.m3u8", seg)))
	return args
}
