package probe

import (
	"testing"

	"homemedia/internal/model"
)

func TestCanCopyVideo(t *testing.T) {
	hd, uhd := CapsForModel(""), CapsForModel("Chromecast Ultra")
	video := func(codec, pix string, level, height int) *model.MediaInfo {
		return &model.MediaInfo{Video: &model.VideoInfo{Codec: codec, PixFmt: pix, Level: level, Height: height}}
	}
	tests := []struct {
		name string
		info *model.MediaInfo
		caps DeviceCaps
		want bool
	}{
		{"1080p h264", video("h264", "yuv420p", 41, 1080), hd, true},
		{"not probed", nil, hd, false},
		{"audio only", &model.MediaInfo{}, hd, false},
		{"hevc", video("hevc", "yuv420p", 120, 1080), uhd, false},
		{"10-bit h264", video("h264", "yuv420p10le", 41, 1080), hd, false},
		{"unknown level", video("h264", "yuv420p", 0, 1080), hd, false},
		{"level too high", video("h264", "yuv420p", 51, 1080), hd, false},
		{"4k on a 1080p device", video("h264", "yuv420p", 42, 2160), hd, false},
		{"4k on a 4k device", video("h264", "yuv420p", 51, 2160), uhd, true},
	}
	for _, tt := range tests {
		if got := CanCopyVideo(tt.info, tt.caps); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
