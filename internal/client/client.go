// Package client is the typed HTTP client for mediad, used by the desktop app.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"homemedia/internal/model"
)

// Info is the response of GET /api/info.
type Info struct {
	Name    string `json:"name"`
	LAN     string `json:"lan"`
	Port    int    `json:"port"`
	Base    string `json:"base"`
	TMDB    bool   `json:"tmdb"`
	Version string `json:"version"`
}

// Device is a Chromecast listed by GET /api/cast/devices.
type Device struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
}

// CastStatus is the response of GET /api/cast/{device}/status.
type CastStatus struct {
	State      string  `json:"state"` // DISCONNECTED | IDLE | LOADING | PLAYING | PAUSED | BUFFERING
	Position   float64 `json:"position"`
	Duration   float64 `json:"duration"`
	ItemID     string  `json:"itemId"`
	Audio      int     `json:"audio"`
	Sub        string  `json:"sub"` // sub track id, "" = off
	Mode       string  `json:"mode"`
	Volume     float64 `json:"volume"`
	Muted      bool    `json:"muted"`
	IdleReason string  `json:"idleReason,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// PlayRequest is the body of POST /api/cast/{device}/play.
type PlayRequest struct {
	ID     string  `json:"id"`
	Audio  int     `json:"audio"`
	Sub    string  `json:"sub"`    // "" = no subtitles
	Start  float64 `json:"start"`  // seconds; > 0 skips the resume question
	Mode   string  `json:"mode"`   // auto | direct | transcode
	Resume string  `json:"resume"` // "" | continue | restart
}

// ResumeError is returned by Play when the server answers 409: the item was
// partly watched and the caller must retry with Resume "continue" or "restart".
type ResumeError struct {
	Position float64
	Duration float64
}

func (e *ResumeError) Error() string {
	return fmt.Sprintf("already watched up to %s (choose continue or restart)", clock(e.Position))
}

// APIError is a non-2xx answer carrying the server's {"error": "..."} text.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mediad: %s (HTTP %d)", e.Message, e.Status)
}

// Client talks to one mediad.
type Client struct {
	Base string // http://host:port, no trailing slash
	HTTP *http.Client
}

// Per-call timeouts.
const (
	timeoutShort = 3 * time.Second
	timeoutPlay  = 60 * time.Second
	timeoutStd   = 15 * time.Second
)

// New returns a client for base (a trailing slash is trimmed).
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{}}
}

// ArtURL turns a relative art path from the library ("/art/ab12/poster?v=1")
// into an absolute URL on this server. nil or "" gives "".
func (c *Client) ArtURL(rel *string) string {
	if rel == nil || *rel == "" {
		return ""
	}
	p := *rel
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return c.Base + p
}

// FetchArt downloads the bytes behind ArtURL(rel).
func (c *Client) FetchArt(ctx context.Context, rel *string) ([]byte, error) {
	u := c.ArtURL(rel)
	if u == "" {
		return nil, errors.New("mediad: no art path")
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutStd)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, data)
	}
	return data, nil
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.call(ctx, timeoutStd, http.MethodGet, "/api/info", nil, &out)
	return out, err
}

func (c *Client) Library(ctx context.Context) (model.Snapshot, error) {
	var out model.Snapshot
	err := c.call(ctx, timeoutStd, http.MethodGet, "/api/library", nil, &out)
	return out, err
}

func (c *Client) Scan(ctx context.Context, refresh bool) error {
	path := "/api/scan"
	if refresh {
		path += "?refresh=1"
	}
	return c.call(ctx, timeoutStd, http.MethodPost, path, nil, nil)
}

func (c *Client) SetWatched(ctx context.Context, id string, watched bool) error {
	body := struct {
		ID      string `json:"id"`
		Watched bool   `json:"watched"`
	}{id, watched}
	return c.call(ctx, timeoutStd, http.MethodPost, "/api/progress", body, nil)
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var out []Device
	err := c.call(ctx, timeoutStd, http.MethodGet, "/api/cast/devices", nil, &out)
	return out, err
}

// LogEntry is one server log line.
type LogEntry struct {
	Seq   int       `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// Logs returns the server log entries with Seq greater than after, and the
// newest Seq on the server. A last below after means the server restarted.
func (c *Client) Logs(ctx context.Context, after int) (entries []LogEntry, last int, err error) {
	var out struct {
		Entries []LogEntry `json:"entries"`
		Last    int        `json:"last"`
	}
	err = c.call(ctx, timeoutShort, http.MethodGet, "/api/logs?after="+strconv.Itoa(after), nil, &out)
	return out.Entries, out.Last, err
}

// Status uses a short (3 s) timeout; it is polled once a second.
func (c *Client) Status(ctx context.Context, device string) (CastStatus, error) {
	var out CastStatus
	path := "/api/cast/" + url.PathEscape(device) + "/status"
	err := c.call(ctx, timeoutShort, http.MethodGet, path, nil, &out)
	return out, err
}

// Play starts an item. It returns *ResumeError on 409 and *APIError on other
// failures. Its timeout is 60 s (the server waits up to 40 s for the TV).
func (c *Client) Play(ctx context.Context, device string, req PlayRequest) (CastStatus, error) {
	var out CastStatus
	path := "/api/cast/" + url.PathEscape(device) + "/play"
	status, data, err := c.send(ctx, timeoutPlay, http.MethodPost, path, req)
	if err != nil {
		return out, err
	}
	if status == http.StatusConflict {
		var rq struct {
			Code     string  `json:"code"`
			Position float64 `json:"position"`
			Duration float64 `json:"duration"`
		}
		if json.Unmarshal(data, &rq) == nil && rq.Code == "resume" {
			return out, &ResumeError{Position: rq.Position, Duration: rq.Duration}
		}
	}
	if status/100 != 2 {
		return out, apiError(status, data)
	}
	if err := decode(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Control sends a transport command. action: pause|resume|seek|stop|volume|mute|sub|audio.
// value is JSON-encoded as the body's "value" (nil = omitted).
func (c *Client) Control(ctx context.Context, device, action string, value any) error {
	body := map[string]any{"action": action}
	if value != nil {
		body["value"] = value
	}
	path := "/api/cast/" + url.PathEscape(device) + "/control"
	return c.call(ctx, timeoutStd, http.MethodPost, path, body, nil)
}

// send performs one request under a timeout. body, if non-nil, is JSON-encoded.
// Non-2xx statuses are returned to the caller, not turned into errors.
func (c *Client) send(ctx context.Context, timeout time.Duration, method, path string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

// call is send plus status checking and optional JSON decoding into out.
func (c *Client) call(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	status, data, err := c.send(ctx, timeout, method, path, body)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return apiError(status, data)
	}
	if out == nil {
		return nil
	}
	return decode(data, out)
}

func decode(data []byte, out any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// apiError builds an *APIError from a non-2xx body: the JSON "error" field if
// present, else the trimmed body text, else the status text.
func apiError(status int, data []byte) *APIError {
	msg := ""
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil {
		msg = env.Error
	}
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Message: msg}
}

// clock formats seconds as m:ss, or h:mm:ss from one hour on.
func clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
