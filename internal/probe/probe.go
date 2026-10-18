// Package probe reads media files with ffprobe and decides how a Chromecast can play them.
// Port of lib/probe.js (node-ref).
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homemedia/internal/model"
)

const probeTimeout = 60 * time.Second

// textSubs are subtitle codecs that can be converted to WebVTT.
var textSubs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true,
	"mov_text": true, "webvtt": true, "text": true,
}

// castVideoContainers are the containers a Chromecast accepts over HTTP range requests.
var castVideoContainers = map[string]bool{".mp4": true, ".m4v": true, ".mov": true}

// ffRaw is the subset of `ffprobe -show_format -show_streams -print_format json` we use.
type ffRaw struct {
	Format  ffFormat   `json:"format"`
	Streams []ffStream `json:"streams"`
}

type ffFormat struct {
	Duration string `json:"duration"`
}

type ffStream struct {
	CodecName     string            `json:"codec_name"`
	CodecType     string            `json:"codec_type"`
	Profile       string            `json:"profile"`
	Level         int               `json:"level"`
	PixFmt        string            `json:"pix_fmt"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	ColorTransfer string            `json:"color_transfer"`
	Channels      int               `json:"channels"`
	Tags          map[string]string `json:"tags"`
	Disposition   map[string]int    `json:"disposition"`
}

// Probe runs `ffprobe -print_format json -show_format -show_streams` on file
// (60 s timeout) and summarizes it. Cover-art "video" streams are ignored.
func Probe(ctx context.Context, file string) (*model.MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json", "-show_format", "-show_streams", file)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("ffprobe %s: %w: %s", file, err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("ffprobe %s: %w", file, err)
	}
	return ParseFFprobe(out)
}

// ParseFFprobe converts raw ffprobe JSON into MediaInfo. Split out so it is
// testable without ffprobe.
func ParseFFprobe(raw []byte) (*model.MediaInfo, error) {
	var r ffRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse ffprobe json: %w", err)
	}

	info := &model.MediaInfo{
		Duration: parseDuration(r.Format.Duration),
		Audio:    []model.AudioTrack{},
		Subs:     []model.EmbeddedSub{},
	}
	for _, s := range r.Streams {
		switch s.CodecType {
		case "video":
			if info.Video == nil && s.Disposition["attached_pic"] == 0 {
				info.Video = &model.VideoInfo{
					Codec:   s.CodecName,
					Profile: s.Profile,
					Level:   s.Level,
					PixFmt:  s.PixFmt,
					Width:   s.Width,
					Height:  s.Height,
					HDR:     strings.Contains(s.ColorTransfer, "smpte2084") || strings.Contains(s.ColorTransfer, "arib-std-b67"),
				}
			}
		case "audio":
			info.Audio = append(info.Audio, model.AudioTrack{
				Index:    len(info.Audio),
				Codec:    s.CodecName,
				Channels: s.Channels,
				Lang:     tagValue(s.Tags, "language"),
				Title:    tagValue(s.Tags, "title"),
				Default:  s.Disposition["default"] != 0,
			})
		case "subtitle":
			info.Subs = append(info.Subs, model.EmbeddedSub{
				Index:  len(info.Subs),
				Codec:  s.CodecName,
				Lang:   tagValue(s.Tags, "language"),
				Title:  tagValue(s.Tags, "title"),
				Forced: s.Disposition["forced"] != 0,
				Text:   textSubs[s.CodecName],
			})
		}
	}
	return info, nil
}

// parseDuration mirrors Number(x) || 0 from probe.js: unparsable or non-finite values become 0.
func parseDuration(s string) float64 {
	d, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(d) || math.IsInf(d, 0) {
		return 0
	}
	return d
}

// tagValue reads a tag by key, trying the exact key first and then any
// case-insensitive match, as probe.js does with the lower and upper case keys.
// Empty values count as missing.
func tagValue(tags map[string]string, key string) string {
	if v := tags[key]; v != "" {
		return v
	}
	if v := tags[strings.ToUpper(key)]; v != "" {
		return v
	}
	for k, v := range tags {
		if v != "" && strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// DeviceCaps describes what a Chromecast model can decode.
type DeviceCaps struct {
	HEVC      bool
	MaxHeight int // 1080 or 2160
	VP9       bool
	MaxLevel  int // H.264 level * 10, e.g. 42
}

// CapsForModel maps the mDNS "md" TXT value (e.g. "Chromecast Ultra") to
// capabilities. Unknown models get the conservative default: H.264 only, 1080p, level 42.
func CapsForModel(model string) DeviceCaps {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "chromecast ultra", "chromecast with google tv":
		return DeviceCaps{HEVC: true, MaxHeight: 2160, VP9: true, MaxLevel: 52}
	}
	return DeviceCaps{HEVC: false, MaxHeight: 1080, VP9: false, MaxLevel: 42}
}

// CanDirectPlay reports whether file can be sent to a device as-is over HTTP
// range requests: mp4/m4v/mov container, h264 yuv420p within caps, exactly one
// AAC or MP3 audio track with <= 6 channels.
func CanDirectPlay(file string, info *model.MediaInfo, caps DeviceCaps) bool {
	if info == nil || info.Video == nil || !castVideoContainers[strings.ToLower(filepath.Ext(file))] {
		return false
	}
	v := info.Video
	if v.PixFmt != "yuv420p" || v.Height > caps.MaxHeight {
		return false
	}
	switch v.Codec {
	case "h264":
		if v.Level <= 0 || v.Level > caps.MaxLevel {
			return false
		}
	case "hevc":
		if !caps.HEVC {
			return false
		}
	default:
		return false
	}
	if len(info.Audio) != 1 {
		return false
	}
	a := info.Audio[0]
	return (a.Codec == "aac" || a.Codec == "mp3") && a.Channels <= 6
}
