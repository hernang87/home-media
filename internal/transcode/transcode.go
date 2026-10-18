// Package transcode produces HLS segments on demand with ffmpeg.
// One ffmpeg per (item, audio track),
// restarted at the requested segment on seeks, throttled when far ahead of the
// player and evicted when idle.
package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"homemedia/internal/model"
)

const (
	// SegSeconds is the HLS segment length.
	SegSeconds = 6
	// ahead is how many segments past the last produced one a running ffmpeg is
	// still considered "close enough" to wait for instead of restarting.
	ahead = 8
	// maxBuffer stops ffmpeg when it is this many segments past the last request.
	maxBuffer = 30
	// keepBehind is how many segments behind the playhead are kept, so a short rewind needs no ffmpeg restart.
	keepBehind = 20
	idleTime   = 10 * time.Minute
	pollEvery  = 100 * time.Millisecond
	waitMax    = 90 * time.Second
)

var segFileRe = regexp.MustCompile(`^seg(\d+)\.ts$`)

// SegmentCount is the number of segments for a duration in seconds.
func SegmentCount(duration float64) int {
	n := int(math.Ceil(duration / SegSeconds))
	if n < 1 {
		return 1
	}
	return n
}

// SegmentName is the file name of segment n.
func SegmentName(n int) string { return fmt.Sprintf("seg%05d.ts", n) }

// Playlist returns the complete VOD playlist for a duration in seconds. It
// needs no ffmpeg, so players can seek anywhere immediately.
func Playlist(duration float64) string {
	count := SegmentCount(duration)
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", SegSeconds)
	for i := 0; i < count; i++ {
		length := float64(SegSeconds)
		if i == count-1 {
			length = duration - float64(SegSeconds*i)
		}
		if length < 0.001 {
			length = 0.001
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", length, SegmentName(i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

type proc struct {
	cmd    *exec.Cmd
	killed atomic.Bool
}

type session struct {
	key      string
	dir      string
	item     model.Item
	audio    int
	mu       sync.Mutex // guards the fields below
	proc     *proc
	startSeg int
	lastReq  int
	touched  time.Time
}

// Manager owns all transcode sessions.
type Manager struct {
	tmpDir string
	hwOK   atomic.Bool

	mu       sync.Mutex
	sessions map[string]*session
	stop     chan struct{}
}

// NewManager clears tmpDir and starts the idle janitor.
func NewManager(tmpDir string) (*Manager, error) {
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{tmpDir: tmpDir, sessions: map[string]*session{}, stop: make(chan struct{})}
	m.hwOK.Store(runtime.GOOS == "darwin")
	go m.janitor()
	return m, nil
}

func (m *Manager) session(item model.Item, audio int) (*session, error) {
	key := fmt.Sprintf("%s_%d", item.ID, audio)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil {
		dir := filepath.Join(m.tmpDir, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		s = &session{key: key, dir: dir, item: item, audio: audio}
		m.sessions[key] = s
	}
	s.mu.Lock()
	s.touched = time.Now()
	s.mu.Unlock()
	return s, nil
}

func ffmpegArgs(item model.Item, audio, seg int, hw bool, dir string) []string {
	start := seg * SegSeconds
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	if hw {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	args = append(args, "-ss", strconv.Itoa(start), "-i", item.Path, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-sn", "-dn")
	args = append(args, "-vf", "scale=w=-2:h='min(1080,ih)':flags=bicubic,format=yuv420p")
	if hw {
		args = append(args, "-c:v", "h264_videotoolbox", "-b:v", "8M", "-maxrate", "10M", "-bufsize", "16M")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-maxrate", "10M", "-bufsize", "20M")
	}
	args = append(args, "-profile:v", "high", "-level", "4.1", "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", SegSeconds))
	args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "192k", "-af", "aresample=async=1:first_pts=0")
	args = append(args, "-output_ts_offset", strconv.Itoa(start))
	args = append(args, "-f", "hls", "-hls_time", strconv.Itoa(SegSeconds), "-hls_list_size", "0", "-hls_flags", "temp_file")
	args = append(args, "-start_number", strconv.Itoa(seg), "-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"), filepath.Join(dir, fmt.Sprintf("ff%d.m3u8", seg)))
	return args
}

// segNames returns the file names in the session dir, or nil.
func (s *session) segNames() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// maxSegment returns the highest finished segment number in names, or -1.
func maxSegment(names []string) int {
	max := -1
	for _, name := range names {
		if m := segFileRe.FindStringSubmatch(name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > max {
				max = n
			}
		}
	}
	return max
}

// staleSegments returns the segment names in names more than keepBehind
// segments behind lastReq.
func staleSegments(names []string, lastReq, keepBehind int) []string {
	var stale []string
	for _, name := range names {
		if m := segFileRe.FindStringSubmatch(name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n < lastReq-keepBehind {
				stale = append(stale, name)
			}
		}
	}
	return stale
}

// producedMax returns the highest finished segment number in the session dir, or -1.
func (s *session) producedMax() int {
	return maxSegment(s.segNames())
}

// killLocked stops the running ffmpeg and its children. s.mu must be held.
func (s *session) killLocked() {
	if p := s.proc; p != nil {
		p.killed.Store(true)
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		s.proc = nil
	}
}

// startLocked (re)starts ffmpeg at seg. s.mu must be held.
func (m *Manager) startLocked(s *session, seg int) {
	s.killLocked()
	hw := m.hwOK.Load()
	cmd := exec.Command("ffmpeg", ffmpegArgs(s.item, s.audio, seg, hw, s.dir)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		slog.Error("ffmpeg start failed", "err", err)
		return
	}
	p := &proc{cmd: cmd}
	s.proc, s.startSeg = p, seg
	began := time.Now()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if s.proc == p {
			s.proc = nil
		}
		s.mu.Unlock()
		if err != nil && !p.killed.Load() {
			tail := stderr.String()
			if len(tail) > 400 {
				tail = tail[len(tail)-400:]
			}
			slog.Error("ffmpeg failed", "title", s.item.Rel, "hw", hw, "err", strings.TrimSpace(tail))
			// The hardware path can be unsupported for some inputs; fall back to software for good.
			if hw && time.Since(began) < 15*time.Second {
				m.hwOK.Store(false)
			}
		}
	}()
}

// worthWaiting reports whether the running ffmpeg will reach segment n soon. s.mu must be held.
func (s *session) worthWaitingLocked(n int) bool {
	if s.proc == nil {
		return false
	}
	next := s.producedMax() + 1
	if s.startSeg > next {
		next = s.startSeg
	}
	return n >= s.startSeg && n <= next+ahead
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Segment returns the path of finished segment n, starting or repositioning
// ffmpeg as needed. It returns ctx.Err() if the caller gives up first.
func (m *Manager) Segment(ctx context.Context, item model.Item, audio, n int) (string, error) {
	s, err := m.session(item, audio)
	if err != nil {
		return "", err
	}
	file := filepath.Join(s.dir, SegmentName(n))
	s.mu.Lock()
	s.lastReq = n
	s.mu.Unlock()
	if exists(file) {
		return file, nil
	}
	starts := 0
	s.mu.Lock()
	if !s.worthWaitingLocked(n) {
		m.startLocked(s, n)
		starts++
	}
	s.mu.Unlock()

	deadline := time.NewTimer(waitMax)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("transcode timed out")
		case <-tick.C:
			if exists(file) {
				return file, nil
			}
			s.mu.Lock()
			if s.proc == nil {
				// ffmpeg ended without producing it (failure, or it was throttled): start again.
				if starts >= 3 {
					s.mu.Unlock()
					return "", errors.New("transcode failed")
				}
				m.startLocked(s, n)
				starts++
			}
			s.mu.Unlock()
		}
	}
}

// StopAll kills every ffmpeg and stops the janitor.
func (m *Manager) StopAll() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.mu.Lock()
		s.killLocked()
		s.mu.Unlock()
	}
}

// StopItem stops transcoding for an item (all audio tracks), e.g. when a cast ends.
func (m *Manager) StopItem(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.item.ID == id {
			s.mu.Lock()
			s.killLocked()
			s.mu.Unlock()
		}
	}
}

// janitor keeps transcoding a bounded distance ahead of playback and drops idle sessions.
func (m *Manager) janitor() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		for key, s := range m.sessions {
			s.mu.Lock()
			switch {
			case time.Since(s.touched) > idleTime:
				s.killLocked()
				_ = os.RemoveAll(s.dir)
				delete(m.sessions, key)
			default:
				names := s.segNames()
				if s.proc != nil && maxSegment(names)-s.lastReq > maxBuffer {
					s.killLocked()
				}
				// Old segments are only needed again after a long rewind; Segment restarts ffmpeg for those.
				for _, name := range staleSegments(names, s.lastReq, keepBehind) {
					_ = os.Remove(filepath.Join(s.dir, name))
				}
			}
			s.mu.Unlock()
		}
		m.mu.Unlock()
	}
}
