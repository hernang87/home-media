// Package subs names subtitle tracks and converts them to WebVTT.
// Port of lib/subs.js (node-ref).
package subs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"homemedia/internal/model"
)

// flagTokens are file-name words that describe the track, not its language.
var flagTokens = map[string]bool{"sdh": true, "hi": true, "forced": true, "cc": true, "full": true}

var (
	isoCode        = regexp.MustCompile(`^[a-z]{2,3}$`)
	timestampComma = regexp.MustCompile(`(\d{1,2}:\d{2}:\d{2}),(\d{3})`)
	assTag         = regexp.MustCompile(`\{\\[^}]*\}`)
	fontTag        = regexp.MustCompile(`(?i)</?font[^>]*>`)
	sdhWord        = regexp.MustCompile(`(?i)sdh`)
	sdhOnly        = regexp.MustCompile(`(?i)^sdh$`)
)

const ffmpegTimeout = 60 * time.Second

// LanguageName returns the English name for an ISO 639-1/2 code (including
// bibliographic aliases like "ger"), or the code itself when unknown.
func LanguageName(code string) string {
	if code == "" {
		return ""
	}
	c := strings.ToLower(code)
	if a, ok := aliases[c]; ok {
		c = a
	}
	if name, ok := languageNames[c]; ok {
		return name
	}
	return code
}

// nodeExtname mirrors Node's path.extname: a leading dot does not start an extension.
func nodeExtname(base string) string {
	i := strings.LastIndexByte(base, '.')
	if i <= 0 {
		return ""
	}
	return base[i:]
}

// DescribeSubFile reads language and SDH/forced flags from a subtitle file name
// such as "SDH.eng.HI.srt" or "movie.es.forced.srt".
func DescribeSubFile(file string) model.SubDescription {
	base := filepath.Base(file)
	base = strings.TrimSuffix(base, nodeExtname(base))
	tokens := strings.FieldsFunc(strings.ToLower(base), func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || unicode.IsSpace(r)
	})

	word := ""
	for i := len(tokens) - 1; i >= 0; i-- {
		if !flagTokens[tokens[i]] {
			word = tokens[i]
			break
		}
	}
	code := word
	if a, ok := aliases[word]; ok {
		code = a
	}

	var d model.SubDescription
	if n, ok := byName[code]; ok && code != "" {
		d.Lang = n
	} else if code != "" && isoCode.MatchString(code) && LanguageName(code) != code {
		d.Lang = code
	}
	for _, t := range tokens {
		if t == "sdh" || t == "hi" {
			d.SDH = true
		}
		if t == "forced" {
			d.Forced = true
		}
	}
	return d
}

// SubLabel builds the label shown to users, e.g. "English (SDH, Forced)".
func SubLabel(d model.SubDescription) string {
	base := LanguageName(d.Lang)
	if base == "" {
		base = d.Title
	}
	if base == "" {
		base = "Unknown"
	}

	var extra []string
	add := func(s string) {
		for _, e := range extra {
			if e == s {
				return
			}
		}
		extra = append(extra, s)
	}
	if d.Title != "" && LanguageName(d.Lang) != "" && !sdhOnly.MatchString(d.Title) {
		add(d.Title)
	}
	if d.SDH || sdhWord.MatchString(d.Title) {
		add("SDH")
	}
	if d.Forced {
		add("Forced")
	}
	if len(extra) == 0 {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, strings.Join(extra, ", "))
}

// SRTToVTT converts SRT text (already decoded) to WebVTT text.
func SRTToVTT(text string) string {
	body := strings.TrimPrefix(text, "\uFEFF")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = timestampComma.ReplaceAllString(body, "${1}.${2}")
	body = assTag.ReplaceAllString(body, "")
	body = fontTag.ReplaceAllString(body, "")
	if strings.HasPrefix(body, "WEBVTT") {
		return body
	}
	return "WEBVTT\n\n" + body
}

// decodeSubtitle returns UTF-8 text when buf is valid UTF-8, else windows-1252.
func decodeSubtitle(buf []byte) (string, error) {
	if utf8.Valid(buf) {
		return string(buf), nil
	}
	// WHATWG windows-1252 (what TextDecoder uses) maps the five unassigned
	// bytes to the C1 controls; charmap would give U+FFFD for them.
	var sb strings.Builder
	for _, b := range buf {
		switch b {
		case 0x81, 0x8D, 0x8F, 0x90, 0x9D:
			sb.WriteRune(rune(b))
		default:
			sb.WriteRune(charmap.Windows1252.DecodeByte(b))
		}
	}
	return sb.String(), nil
}

// ffmpegBin finds ffmpeg on PATH, falling back to the Homebrew location.
func ffmpegBin() (string, error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	const brew = "/opt/homebrew/bin/ffmpeg"
	if _, err := os.Stat(brew); err == nil {
		return brew, nil
	}
	return "", fmt.Errorf("subs: ffmpeg not found")
}

// runFFmpeg runs ffmpeg with args (60 s limit) and returns its stdout.
func runFFmpeg(args ...string) (string, error) {
	bin, err := ffmpegBin()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// LoadVTT returns WebVTT for an external or embedded subtitle of the item at
// videoPath. External .srt/.vtt are decoded (UTF-8, else windows-1252) and
// converted in Go; everything else goes through `ffmpeg -f webvtt -`.
func LoadVTT(videoPath string, sub model.SubTrack) (string, error) {
	if sub.Source == "embedded" {
		return runFFmpeg("-v", "error", "-i", videoPath, "-map", fmt.Sprintf("0:s:%d", sub.Index), "-f", "webvtt", "-")
	}
	ext := strings.ToLower(nodeExtname(filepath.Base(sub.File)))
	if ext == ".srt" || ext == ".vtt" {
		buf, err := os.ReadFile(sub.File)
		if err != nil {
			return "", err
		}
		text, err := decodeSubtitle(buf)
		if err != nil {
			return "", err
		}
		return SRTToVTT(text), nil
	}
	return runFFmpeg("-v", "error", "-i", sub.File, "-f", "webvtt", "-")
}
