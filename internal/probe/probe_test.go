package probe

import (
	"testing"

	"homemedia/internal/model"
)

func TestCanDirectPlay(t *testing.T) {
	good := &model.MediaInfo{
		Duration: 60,
		Video:    &model.VideoInfo{Codec: "h264", PixFmt: "yuv420p", Level: 41, Height: 1080},
		Audio:    []model.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	caps := CapsForModel("Unknown device")
	if !CanDirectPlay("a.mp4", good, caps) {
		t.Error("h264/aac mp4 should direct play")
	}
	if CanDirectPlay("a.mkv", good, caps) {
		t.Error("mkv container should not direct play")
	}
	hevc := *good
	hevc.Video = &model.VideoInfo{Codec: "hevc", PixFmt: "yuv420p", Level: 120, Height: 1080}
	if CanDirectPlay("a.mp4", &hevc, caps) {
		t.Error("hevc should not direct play on unknown device")
	}
	if !CapsForModel("Chromecast Ultra").HEVC {
		t.Error("Chromecast Ultra decodes HEVC")
	}
}
