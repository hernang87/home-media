// Package parse turns release-style file names into library metadata.
package parse

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"homemedia/internal/model"
)

// jsSpace is the body of a character class matching exactly what JavaScript's
// \s matches (Go's \s is ASCII-only, so it is spelled out).
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	wsClass  = "[" + jsSpace + "]"
	sepClass = "[" + jsSpace + `._-]`

	// Go's (?i) folds a few non-ASCII letters (e.g. U+017F, U+212A) onto ASCII
	// ones, which JS non-unicode /i does not. Only affects non-ASCII input.
	reQuality = regexp.MustCompile(`(?i)\b(480p|576p|720p|1080p|2160p|4k|uhd|bluray|blu-ray|bdrip|brrip|web-?dl|web-?rip|webrip|hdtv|hdrip|dvdrip|x264|x265|h\.?264|h\.?265|hevc|remux|amzn|nf|dsnp)\b`)
	reEpisode = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]?E(\d{1,3})(?:[ ._-]?E?(\d{1,3}))?\b`),
		regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`),
	}
	reSeasonDir   = regexp.MustCompile(`(?i)^(season|temporada|s)[ ._-]?\d+$`)
	reDotsUnders  = regexp.MustCompile(`[._]+`)
	reSpaces      = regexp.MustCompile(wsClass + `+`)
	reBracket     = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
	reBracketYear = regexp.MustCompile(`^[\[(]` + wsClass + `*(?:19|20)\d{2}` + wsClass + `*[\])]$`)
	// JS: /(?:^|[\s._-])((?:19|20)\d{2})(?=$|[\s._-])/g. The lookahead is
	// emulated in findYears because RE2 has no lookaround.
	reYear = regexp.MustCompile(`(?:^|` + sepClass + `)((?:19|20)\d{2})`)
)

type yearHit struct {
	index int // byte offset of the match start (including the separator)
	year  int
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func isSep(r rune) bool {
	return r == '.' || r == '_' || r == '-' || isJSSpace(r)
}

// tidy: "._" become spaces, whitespace runs collapse, then leading/trailing
// whitespace and dashes are trimmed.
func tidy(s string) string {
	s = reDotsUnders.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimFunc(s, func(r rune) bool { return r == '-' || isJSSpace(r) })
}

// findYears returns every (19|20)dd year that is preceded by start-of-text or a
// separator and followed by end-of-text or a separator, matching JS
// matchAll semantics (the lookahead does not consume the trailing separator).
func findYears(text string) []yearHit {
	var hits []yearHit
	for _, loc := range reYear.FindAllStringSubmatchIndex(text, -1) {
		if end := loc[1]; end < len(text) {
			r, _ := utf8.DecodeRuneInString(text[end:])
			if !isSep(r) {
				continue
			}
		}
		y, _ := strconv.Atoi(text[loc[2]:loc[3]])
		hits = append(hits, yearHit{index: loc[0], year: y})
	}
	return hits
}

// CleanTitle strips quality tags, groups and separators from a raw name and
// returns the title plus the release year when one is present.
func CleanTitle(raw string) (title string, year *int) {
	// Drop [groups] and (groups) but keep a bracketed year.
	text := reBracket.ReplaceAllStringFunc(raw, func(m string) string {
		if reBracketYear.MatchString(m) {
			return " " + m[1:len(m)-1] + " "
		}
		return " "
	})
	years := findYears(text)
	// A year at the very start is more likely part of the title ("2012", "1917").
	var pick *yearHit
	for i := range years {
		if years[i].index > 0 {
			pick = &years[i]
		}
	}
	title = text
	if pick != nil {
		y := pick.year
		year = &y
		title = text[:pick.index]
	} else {
		if loc := reQuality.FindStringIndex(text); loc != nil {
			title = text[:loc[0]]
		}
		if len(years) > 0 {
			y := years[0].year
			year = &y
		}
	}
	title = tidy(title)
	if title == "" {
		title = tidy(raw)
	}
	return title, year
}

// jsBasename mirrors Node's path.basename(p) with no extension argument.
func jsBasename(p string) string {
	sep := string(filepath.Separator)
	p = strings.TrimRight(p, sep)
	if i := strings.LastIndex(p, sep); i >= 0 {
		return p[i+len(sep):]
	}
	return p
}

// jsExtname mirrors Node's path.extname for a bare file name.
func jsExtname(b string) string {
	i := strings.LastIndex(b, ".")
	if i <= 0 || b == ".." {
		return ""
	}
	return b[i:]
}

// ParseMediaPath works out what a media file is from its path relative to the
// library root (movie vs episode, title, year, season, episode).
func ParseMediaPath(rel string) model.Parsed {
	sep := string(filepath.Separator)
	parts := strings.Split(rel, sep)
	name := jsBasename(rel)
	base := strings.TrimSuffix(name, jsExtname(name))
	dirs := parts[:len(parts)-1]

	for _, re := range reEpisode {
		loc := re.FindStringSubmatchIndex(base)
		if loc == nil {
			continue
		}
		showRaw := base[:loc[0]]
		if tidy(showRaw) == "" {
			// Nearest ancestor directory that is not a season folder.
			dir := ""
			for i := len(dirs) - 1; i >= 0; i-- {
				if !reSeasonDir.MatchString(dirs[i]) {
					dir = dirs[i]
					break
				}
			}
			if dir != "" {
				showRaw = dir
			} else {
				showRaw = base
			}
		}
		show, showYear := CleanTitle(showRaw)
		season, _ := strconv.Atoi(base[loc[2]:loc[3]])
		episode, _ := strconv.Atoi(base[loc[4]:loc[5]])
		return model.Parsed{
			Type:     model.TypeEpisode,
			Show:     show,
			ShowYear: showYear,
			Season:   season,
			Episode:  episode,
		}
	}

	fromTitle, fromYear := CleanTitle(base)
	if fromYear == nil && len(dirs) > 0 {
		if folder := dirs[len(dirs)-1]; folder != "" {
			ft, fy := CleanTitle(folder)
			if fy != nil {
				return model.Parsed{Type: model.TypeMovie, Title: ft, Year: fy}
			}
		}
	}
	return model.Parsed{Type: model.TypeMovie, Title: fromTitle, Year: fromYear}
}
