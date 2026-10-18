// Package model holds the types shared across packages. Frozen in M0: change
// only with the main model's approval, because parallel agents depend on it.
// JSON tags match the Node prototype so its data/library.json shape is kept.
package model

// MediaType is the kind of library entry a file was parsed as.
type MediaType string

const (
	TypeMovie   MediaType = "movie"
	TypeEpisode MediaType = "episode"
)

// Parsed is the result of parsing a path relative to the media root.
// Movie: Title/Year. Episode: Show/ShowYear/Season/Episode.
type Parsed struct {
	Type     MediaType `json:"type"`
	Title    string    `json:"title,omitempty"`
	Year     *int      `json:"year,omitempty"`
	Show     string    `json:"show,omitempty"`
	ShowYear *int      `json:"showYear"`
	Season   int       `json:"season,omitempty"`
	Episode  int       `json:"episode,omitempty"`
}

// VideoInfo is the first non-cover video stream from ffprobe.
type VideoInfo struct {
	Codec   string `json:"codec"`
	Profile string `json:"profile"`
	Level   int    `json:"level"`
	PixFmt  string `json:"pixFmt"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	HDR     bool   `json:"hdr"`
}

// AudioTrack is one audio stream; Index counts audio streams only (0:a:Index).
type AudioTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Lang     string `json:"lang"`
	Title    string `json:"title"`
	Default  bool   `json:"default"`
}

// EmbeddedSub is one subtitle stream; Index counts subtitle streams only (0:s:Index).
// Text is false for image formats (PGS, VobSub) that cannot become WebVTT.
type EmbeddedSub struct {
	Index  int    `json:"index"`
	Codec  string `json:"codec"`
	Lang   string `json:"lang"`
	Title  string `json:"title"`
	Forced bool   `json:"forced"`
	Text   bool   `json:"text"`
}

// MediaInfo is the ffprobe summary of one file. Video is nil for audio-only files.
type MediaInfo struct {
	Duration float64       `json:"duration"` // seconds
	Video    *VideoInfo    `json:"video"`
	Audio    []AudioTrack  `json:"audio"`
	Subs     []EmbeddedSub `json:"subs"`
}

// SubTrack is a subtitle the player can offer, external file or embedded stream.
type SubTrack struct {
	ID     string `json:"id"`     // "e0".. for external, "i0".. for embedded
	Source string `json:"source"` // "external" | "embedded"
	File   string `json:"file,omitempty"`
	Index  int    `json:"index,omitempty"` // embedded stream index
	Lang   string `json:"lang"`
	Label  string `json:"label"`
	Forced bool   `json:"forced"`
}

// SubDescription is what a subtitle filename or stream tells us about itself.
type SubDescription struct {
	Lang   string `json:"lang"`
	Title  string `json:"title,omitempty"`
	SDH    bool   `json:"sdh"`
	Forced bool   `json:"forced"`
}

// CastMember is one credited actor.
type CastMember struct {
	Name  string  `json:"name"`
	Role  string  `json:"role"`
	Photo *string `json:"photo"`
}

// Meta is TMDB metadata for a movie, show or episode.
type Meta struct {
	Source       string       `json:"source"`
	TmdbID       int          `json:"tmdbId,omitempty"`
	Title        string       `json:"title"`
	Year         *int         `json:"year,omitempty"`
	Overview     string       `json:"overview"`
	Tagline      string       `json:"tagline,omitempty"`
	Rating       *float64     `json:"rating"`
	Genres       []string     `json:"genres"`
	Runtime      int          `json:"runtime,omitempty"` // minutes
	PosterPath   string       `json:"posterPath,omitempty"`
	BackdropPath string       `json:"backdropPath,omitempty"`
	Cast         []CastMember `json:"cast"`
}

// EpisodeMeta is TMDB metadata for one episode of a season.
type EpisodeMeta struct {
	Title     string   `json:"title"`
	Overview  string   `json:"overview"`
	Runtime   int      `json:"runtime"`
	StillPath string   `json:"stillPath"`
	Rating    *float64 `json:"rating"`
}

// Art says which images exist on disk for an item or show.
type Art struct {
	Poster   bool `json:"poster,omitempty"`
	Backdrop bool `json:"backdrop,omitempty"`
	Thumb    bool `json:"thumb,omitempty"` // backdrop is a video frame, not TMDB art
}

// Item is one video file in the library (cache record).
type Item struct {
	ID         string     `json:"id"`
	Path       string     `json:"path"`
	Rel        string     `json:"rel"`
	Size       int64      `json:"size"`
	MTime      float64    `json:"mtime"` // ms
	Added      float64    `json:"added"` // ms
	Parsed     Parsed     `json:"parsed"`
	Info       *MediaInfo `json:"info"`
	SubFiles   []string   `json:"subFiles"`
	Subs       []SubTrack `json:"subs"`
	Meta       *Meta      `json:"meta,omitempty"`
	TriedAt    int64      `json:"triedAt,omitempty"` // unix ms of last metadata attempt
	Art        Art        `json:"art"`
	DirectPlay bool       `json:"directPlay"`
}

// Show groups episodes by Key (slug of the parsed show name).
type Show struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Year    *int   `json:"year"`
	Meta    *Meta  `json:"meta,omitempty"`
	TriedAt int64  `json:"triedAt,omitempty"`
	Art     Art    `json:"art"`
}

// Progress is the per-item watch state.
type Progress struct {
	Pos     int     `json:"pos"` // seconds
	Dur     float64 `json:"dur"`
	Watched bool    `json:"watched"`
	T       int64   `json:"t"` // unix ms
}

// ShowRef is the short show reference inside a PublicItem.
type ShowRef struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// PublicSub is a SubTrack without server-side paths.
type PublicSub struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Lang   string `json:"lang"`
	Source string `json:"source"`
	Forced bool   `json:"forced"`
}

// PublicProgress is Progress as the clients see it.
type PublicProgress struct {
	Pos     int   `json:"pos"`
	Watched bool  `json:"watched"`
	T       int64 `json:"t"`
}

// PublicItem is the API shape of an item.
type PublicItem struct {
	ID       string          `json:"id"`
	Type     MediaType       `json:"type"`
	Title    string          `json:"title"`
	Year     *int            `json:"year"`
	Show     *ShowRef        `json:"show"`
	Season   *int            `json:"season"`
	Episode  *int            `json:"episode"`
	Overview string          `json:"overview"`
	Tagline  string          `json:"tagline"`
	Rating   *float64        `json:"rating"`
	Genres   []string        `json:"genres"`
	Cast     []CastMember    `json:"cast"`
	Runtime  int             `json:"runtime"` // minutes
	Duration float64         `json:"duration"`
	Added    float64         `json:"added"`
	Size     int64           `json:"size"`
	File     string          `json:"file"`
	Poster   *string         `json:"poster"`
	Backdrop *string         `json:"backdrop"`
	Video    *VideoInfo      `json:"video"`
	Audio    []AudioTrack    `json:"audio"`
	Subs     []PublicSub     `json:"subs"`
	PlayMode string          `json:"playMode"` // "direct" | "transcode"
	Progress *PublicProgress `json:"progress"`
}

// PublicShow is the API shape of a show; Episodes holds item IDs ordered by season, episode.
type PublicShow struct {
	Key      string       `json:"key"`
	Title    string       `json:"title"`
	Year     *int         `json:"year"`
	Overview string       `json:"overview"`
	Rating   *float64     `json:"rating"`
	Genres   []string     `json:"genres"`
	Cast     []CastMember `json:"cast"`
	Poster   *string      `json:"poster"`
	Backdrop *string      `json:"backdrop"`
	Added    float64      `json:"added"`
	Episodes []string     `json:"episodes"`
}

// ScanStatus reports scanner progress.
type ScanStatus struct {
	Scanning bool   `json:"scanning"`
	Phase    string `json:"phase"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
}

// Snapshot is the response of GET /api/library.
type Snapshot struct {
	Items  []PublicItem `json:"items"`
	Shows  []PublicShow `json:"shows"`
	Status ScanStatus   `json:"status"`
}
