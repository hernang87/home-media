package probe

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"homemedia/internal/model"
)

const multiAudioJSON = `{
  "streams": [
    {"index":0,"codec_name":"h264","codec_type":"video","profile":"High","level":41,"pix_fmt":"yuv420p","width":1920,"height":1080,"disposition":{"default":1,"attached_pic":0}},
    {"index":1,"codec_name":"aac","codec_type":"audio","channels":6,"tags":{"language":"eng","title":"5.1 Surround"},"disposition":{"default":1}},
    {"index":2,"codec_name":"ac3","codec_type":"audio","channels":2,"tags":{"LANGUAGE":"jpn","TITLE":"Stereo"},"disposition":{"default":0}},
    {"index":3,"codec_name":"mp3","codec_type":"audio","channels":2,"tags":{"language":"spa"}}
  ],
  "format": {"duration":"5423.104000"}
}`

const embeddedSubsJSON = `{
  "streams": [
    {"index":0,"codec_name":"h264","codec_type":"video","profile":"Main","level":40,"pix_fmt":"yuv420p","width":1280,"height":720,"disposition":{"attached_pic":0}},
    {"index":1,"codec_name":"aac","codec_type":"audio","channels":2,"tags":{"language":"eng"},"disposition":{"default":1}},
    {"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"eng","title":"Forced"},"disposition":{"forced":1}},
    {"index":3,"codec_name":"hdmv_pgs_subtitle","codec_type":"subtitle","tags":{"language":"eng","title":"SDH"},"disposition":{"forced":0,"default":1}},
    {"index":4,"codec_name":"mov_text","codec_type":"subtitle","tags":{"LANGUAGE":"fra"}}
  ],
  "format": {"duration":"100.000000"}
}`

const coverArtJSON = `{
  "streams": [
    {"index":0,"codec_name":"mjpeg","codec_type":"video","profile":"Baseline","level":-99,"pix_fmt":"yuvj420p","width":600,"height":600,"disposition":{"attached_pic":1}},
    {"index":1,"codec_name":"h264","codec_type":"video","profile":"High","level":31,"pix_fmt":"yuv420p","width":1280,"height":720,"disposition":{"attached_pic":0}},
    {"index":2,"codec_name":"aac","codec_type":"audio","channels":2}
  ],
  "format": {"duration":"60.5"}
}`

const coverArtOnlyJSON = `{
  "streams": [
    {"index":0,"codec_name":"png","codec_type":"video","width":300,"height":300,"disposition":{"attached_pic":1}},
    {"index":1,"codec_name":"mp3","codec_type":"audio","channels":2}
  ],
  "format": {"duration":"180.0"}
}`

func hdrJSON(transfer string) string {
	return `{
  "streams": [
    {"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"pix_fmt":"yuv420p10le","width":3840,"height":2160,"color_transfer":"` + transfer + `","disposition":{"attached_pic":0}},
    {"index":1,"codec_name":"eac3","codec_type":"audio","channels":6}
  ],
  "format": {"duration":"7200.000000"}
}`
}

func mustParse(t *testing.T, raw string) *model.MediaInfo {
	t.Helper()
	info, err := ParseFFprobe([]byte(raw))
	if err != nil {
		t.Fatalf("ParseFFprobe: %v", err)
	}
	return info
}

func TestParseFFprobeMultiAudio(t *testing.T) {
	info := mustParse(t, multiAudioJSON)

	if info.Duration != 5423.104 {
		t.Errorf("duration = %v, want 5423.104", info.Duration)
	}
	want := &model.VideoInfo{Codec: "h264", Profile: "High", Level: 41, PixFmt: "yuv420p", Width: 1920, Height: 1080}
	if !reflect.DeepEqual(info.Video, want) {
		t.Errorf("video = %+v, want %+v", info.Video, want)
	}
	wantAudio := []model.AudioTrack{
		{Index: 0, Codec: "aac", Channels: 6, Lang: "eng", Title: "5.1 Surround", Default: true},
		{Index: 1, Codec: "ac3", Channels: 2, Lang: "jpn", Title: "Stereo", Default: false},
		{Index: 2, Codec: "mp3", Channels: 2, Lang: "spa", Title: "", Default: false},
	}
	if !reflect.DeepEqual(info.Audio, wantAudio) {
		t.Errorf("audio = %+v, want %+v", info.Audio, wantAudio)
	}
	if len(info.Subs) != 0 {
		t.Errorf("subs = %+v, want none", info.Subs)
	}
}

func TestParseFFprobeEmbeddedSubs(t *testing.T) {
	info := mustParse(t, embeddedSubsJSON)

	wantSubs := []model.EmbeddedSub{
		{Index: 0, Codec: "subrip", Lang: "eng", Title: "Forced", Forced: true, Text: true},
		{Index: 1, Codec: "hdmv_pgs_subtitle", Lang: "eng", Title: "SDH", Forced: false, Text: false},
		{Index: 2, Codec: "mov_text", Lang: "fra", Title: "", Forced: false, Text: true},
	}
	if !reflect.DeepEqual(info.Subs, wantSubs) {
		t.Errorf("subs = %+v, want %+v", info.Subs, wantSubs)
	}
	if info.Video == nil || info.Video.Height != 720 {
		t.Errorf("video = %+v, want 720p h264", info.Video)
	}
}

func TestParseFFprobeCoverArtIgnored(t *testing.T) {
	info := mustParse(t, coverArtJSON)
	if info.Video == nil {
		t.Fatal("video is nil, want the h264 stream")
	}
	if info.Video.Codec != "h264" || info.Video.Height != 720 || info.Video.Level != 31 {
		t.Errorf("video = %+v, want h264 720p level 31 (cover art mjpeg skipped)", info.Video)
	}
	if info.Duration != 60.5 {
		t.Errorf("duration = %v, want 60.5", info.Duration)
	}
}

func TestParseFFprobeCoverArtOnlyIsAudioFile(t *testing.T) {
	info := mustParse(t, coverArtOnlyJSON)
	if info.Video != nil {
		t.Errorf("video = %+v, want nil for audio file with cover art", info.Video)
	}
	if len(info.Audio) != 1 || info.Audio[0].Codec != "mp3" {
		t.Errorf("audio = %+v, want one mp3 track", info.Audio)
	}
	if info.Duration != 180 {
		t.Errorf("duration = %v, want 180", info.Duration)
	}
}

func TestParseFFprobeHDR(t *testing.T) {
	tests := []struct {
		transfer string
		want     bool
	}{
		{"smpte2084", true},
		{"arib-std-b67", true},
		{"bt709", false},
		{"", false},
	}
	for _, tc := range tests {
		info := mustParse(t, hdrJSON(tc.transfer))
		if info.Video == nil {
			t.Fatalf("%q: video is nil", tc.transfer)
		}
		if info.Video.HDR != tc.want {
			t.Errorf("color_transfer %q: HDR = %v, want %v", tc.transfer, info.Video.HDR, tc.want)
		}
		if info.Video.Codec != "hevc" || info.Video.PixFmt != "yuv420p10le" {
			t.Errorf("video = %+v, want hevc yuv420p10le", info.Video)
		}
	}
}

func TestParseFFprobeDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want float64
	}{
		{`{"format":{"duration":"12.5"}}`, 12.5},
		{`{"format":{"duration":"N/A"}}`, 0},
		{`{"format":{"duration":"NaN"}}`, 0},
		{`{"format":{}}`, 0},
		{`{}`, 0},
	}
	for _, tc := range tests {
		info := mustParse(t, tc.raw)
		if info.Duration != tc.want {
			t.Errorf("%s: duration = %v, want %v", tc.raw, info.Duration, tc.want)
		}
	}
}

func TestParseFFprobeInvalidJSON(t *testing.T) {
	if _, err := ParseFFprobe([]byte("{not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// directPlayBase is an h264/aac 1080p mp4 that CanDirectPlay accepts on the default caps.
func directPlayBase() *model.MediaInfo {
	return &model.MediaInfo{
		Duration: 60,
		Video:    &model.VideoInfo{Codec: "h264", Profile: "High", Level: 41, PixFmt: "yuv420p", Width: 1920, Height: 1080},
		Audio:    []model.AudioTrack{{Index: 0, Codec: "aac", Channels: 2}},
	}
}

func TestCanDirectPlayRejections(t *testing.T) {
	defaultCaps := CapsForModel("Chromecast")
	ultraCaps := CapsForModel("Chromecast Ultra")

	tests := []struct {
		name    string
		file    string
		nilInfo bool
		mutate  func(*model.MediaInfo)
		caps    DeviceCaps
		want    bool
	}{
		{name: "accepted h264 aac mp4", file: "a.mp4", want: true},
		{name: "accepted uppercase extension", file: "A.MP4", want: true},
		{name: "accepted m4v", file: "a.m4v", want: true},
		{name: "accepted mov", file: "a.mov", want: true},
		{name: "accepted mp3 audio", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio[0].Codec = "mp3"
		}, want: true},
		{name: "accepted 5.1 aac", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio[0].Channels = 6
		}, want: true},
		{name: "accepted level equal to cap", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Level = 42
		}, want: true},

		{name: "nil info", file: "a.mp4", nilInfo: true, want: false},
		{name: "no video stream", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video = nil
		}, want: false},
		{name: "mkv container", file: "a.mkv", want: false},
		{name: "avi container", file: "a.avi", want: false},
		{name: "no extension", file: "a", want: false},
		{name: "codec vp9", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Codec = "vp9"
		}, want: false},
		{name: "hevc on default caps", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Codec = "hevc"
			m.Video.Level = 120
		}, want: false},
		{name: "hevc on ultra caps", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Codec = "hevc"
			m.Video.Level = 153
			m.Video.Height = 2160
		}, caps: ultraCaps, want: true},
		{name: "10-bit pix fmt", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.PixFmt = "yuv420p10le"
		}, want: false},
		{name: "level zero", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Level = 0
		}, want: false},
		{name: "level above default cap", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Level = 51
		}, want: false},
		{name: "level above default cap but within ultra cap", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Level = 51
		}, caps: ultraCaps, want: true},
		{name: "height above default cap", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Height = 1440
		}, want: false},
		{name: "2160p on default caps", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Video.Height = 2160
		}, want: false},
		{name: "no audio tracks", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio = nil
		}, want: false},
		{name: "two audio tracks", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio = append(m.Audio, model.AudioTrack{Index: 1, Codec: "aac", Channels: 2})
		}, want: false},
		{name: "ac3 audio", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio[0].Codec = "ac3"
		}, want: false},
		{name: "opus audio", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio[0].Codec = "opus"
		}, want: false},
		{name: "7.1 audio", file: "a.mp4", mutate: func(m *model.MediaInfo) {
			m.Audio[0].Channels = 8
		}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var info *model.MediaInfo
			if !tc.nilInfo {
				info = directPlayBase()
				if tc.mutate != nil {
					tc.mutate(info)
				}
			}
			caps := tc.caps
			if caps == (DeviceCaps{}) {
				caps = defaultCaps
			}
			if got := CanDirectPlay(tc.file, info, caps); got != tc.want {
				t.Errorf("CanDirectPlay(%q) = %v, want %v", tc.file, got, tc.want)
			}
		})
	}
}

func TestCapsForModel(t *testing.T) {
	tests := []struct {
		md   string
		want DeviceCaps
	}{
		{"Chromecast Ultra", DeviceCaps{HEVC: true, MaxHeight: 2160, VP9: true, MaxLevel: 52}},
		{"chromecast ultra", DeviceCaps{HEVC: true, MaxHeight: 2160, VP9: true, MaxLevel: 52}},
		{"Chromecast with Google TV", DeviceCaps{HEVC: true, MaxHeight: 2160, VP9: true, MaxLevel: 52}},
		{"  CHROMECAST WITH GOOGLE TV ", DeviceCaps{HEVC: true, MaxHeight: 2160, VP9: true, MaxLevel: 52}},
		{"Chromecast", DeviceCaps{HEVC: false, MaxHeight: 1080, VP9: false, MaxLevel: 42}},
		{"Google Nest Hub", DeviceCaps{HEVC: false, MaxHeight: 1080, VP9: false, MaxLevel: 42}},
		{"Unknown device", DeviceCaps{HEVC: false, MaxHeight: 1080, VP9: false, MaxLevel: 42}},
		{"", DeviceCaps{HEVC: false, MaxHeight: 1080, VP9: false, MaxLevel: 42}},
	}
	for _, tc := range tests {
		if got := CapsForModel(tc.md); got != tc.want {
			t.Errorf("CapsForModel(%q) = %+v, want %+v", tc.md, got, tc.want)
		}
	}
}

func TestProbeGeneratedClip(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	out := filepath.Join(t.TempDir(), "out.mp4")
	gen := exec.Command(ffmpegPath, "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=d=2",
		"-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", out)
	if b, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}

	info, err := Probe(context.Background(), out)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if math.Abs(info.Duration-2) > 0.5 {
		t.Errorf("duration = %v, want about 2", info.Duration)
	}
	if info.Video == nil || info.Video.Codec != "h264" || info.Video.PixFmt != "yuv420p" {
		t.Fatalf("video = %+v, want h264 yuv420p", info.Video)
	}
	if len(info.Audio) != 1 || info.Audio[0].Codec != "aac" {
		t.Errorf("audio = %+v, want one aac track", info.Audio)
	}
	if !CanDirectPlay(out, info, CapsForModel("Chromecast")) {
		t.Errorf("generated h264/aac mp4 should direct play: %+v", info)
	}

	if _, err := Probe(context.Background(), filepath.Join(t.TempDir(), "missing.mp4")); err == nil {
		t.Error("Probe on missing file should fail")
	}
}
