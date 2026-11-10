package cast

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homemedia/internal/model"
)

// call sends one request to the cast routes and decodes the JSON answer.
func call(t *testing.T, m *Manager, method, target, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	m.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: bad JSON %q", method, target, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestRoutesErrors(t *testing.T) {
	m, _, _ := setup(t, testItem("a.mp4", h264, aac))
	tests := []struct {
		name, method, target, body string
		want                       int
	}{
		{"status of an unknown device", "GET", "/api/cast/nope/status", "", 404},
		{"play on an unknown device", "POST", "/api/cast/nope/play", `{"id":"abc123"}`, 404},
		{"play with a broken body", "POST", "/api/cast/dev1/play", `{`, 400},
		{"play an unknown item", "POST", "/api/cast/dev1/play", `{"id":"zzz"}`, 400},
		{"play with a bad resume value", "POST", "/api/cast/dev1/play", `{"id":"abc123","resume":"maybe"}`, 400},
		{"play with an unknown subtitle", "POST", "/api/cast/dev1/play", `{"id":"abc123","sub":"zz"}`, 400},
		{"control with a broken body", "POST", "/api/cast/dev1/control", `nope`, 400},
		{"control on an unknown device", "POST", "/api/cast/nope/control", `{"action":"pause"}`, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := call(t, m, tt.method, tt.target, tt.body)
			if code != tt.want || body["error"] == "" {
				t.Errorf("got %d %v, want %d with an error", code, body, tt.want)
			}
		})
	}
}

func TestRoutesDevicesAndStatus(t *testing.T) {
	m, _, _ := setup(t, testItem("a.mp4", h264, aac))
	mux := http.NewServeMux()
	m.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/cast/devices", nil))
	var devices []Device
	if err := json.Unmarshal(rec.Body.Bytes(), &devices); err != nil || len(devices) != 1 || devices[0].Name != "Living Room" {
		t.Fatalf("devices = %s (%v)", rec.Body.String(), err)
	}
	// By percent-encoded name; a device nobody connected to is DISCONNECTED.
	code, st := call(t, m, "GET", "/api/cast/Living%20Room/status", "")
	if code != 200 || st["state"] != string(model.StateDisconnected) {
		t.Errorf("status = %d %v", code, st)
	}
}

func TestRoutesPlayResumeAndControl(t *testing.T) {
	m, dev, lib := setup(t, testItem("a.mp4", h264, aac))
	lib.prog, lib.hasProg = model.Progress{Pos: 300, Dur: 1000}, true

	code, body := call(t, m, "POST", "/api/cast/dev1/play", `{"id":"abc123"}`)
	if code != 409 || body["code"] != "resume" || body["position"] != 300.0 || body["duration"] != 1000.0 {
		t.Fatalf("play without a resume choice = %d %v, want the 409 resume question", code, body)
	}
	if dev.count("LOAD") != 0 {
		t.Error("nothing may be loaded before the choice is made")
	}

	code, body = call(t, m, "POST", "/api/cast/dev1/play", `{"id":"abc123","resume":"continue"}`)
	if code != 200 || body["state"] != "PLAYING" || body["itemId"] != "abc123" {
		t.Fatalf("play resume=continue = %d %v", code, body)
	}
	if got := dev.last("LOAD")["currentTime"]; got != 300.0 {
		t.Errorf("LOAD currentTime = %v, want the saved position", got)
	}

	controls := []struct {
		name, body string
		want       int
		sent       string // message type the device must have received
	}{
		{"mute", `{"action":"mute","value":true}`, 200, "SET_VOLUME"},
		{"mute needs a boolean", `{"action":"mute","value":"yes"}`, 400, ""},
		{"volume", `{"action":"volume","value":0.4}`, 200, "SET_VOLUME"},
		{"volume out of range", `{"action":"volume","value":2}`, 400, ""},
		{"volume needs a number", `{"action":"volume","value":"loud"}`, 400, ""},
		{"seek needs a number", `{"action":"seek"}`, 400, ""},
		{"pause", `{"action":"pause"}`, 200, "PAUSE"},
		{"unknown action", `{"action":"dance"}`, 400, ""},
		{"unknown subtitle", `{"action":"sub","value":"zz"}`, 400, ""},
	}
	for _, tt := range controls {
		t.Run(tt.name, func(t *testing.T) {
			before := dev.count(tt.sent)
			code, body := call(t, m, "POST", "/api/cast/dev1/control", tt.body)
			if code != tt.want {
				t.Fatalf("got %d %v, want %d", code, body, tt.want)
			}
			if tt.sent != "" {
				waitFor(t, func() bool { return dev.count(tt.sent) > before })
			}
		})
	}
	if vol := dev.last("SET_VOLUME")["volume"].(map[string]any); vol["level"] != 0.4 {
		t.Errorf("last SET_VOLUME = %v", vol)
	}
}

func TestSwitchAudioReloadsOverHLS(t *testing.T) {
	// A file that plays directly has to move to HLS to change the audio track.
	m, dev, _ := setup(t, testItem("a.mp4", h264, aac))
	item, _ := m.lib.Get("abc123")
	item.Info.Audio = append(item.Info.Audio, aac)
	m.lib.(*fakeLib).item = item
	if code, body := call(t, m, "POST", "/api/cast/dev1/play", `{"id":"abc123","mode":"transcode","start":40}`); code != 200 {
		t.Fatalf("play = %d %v", code, body)
	}
	if code, body := call(t, m, "POST", "/api/cast/dev1/control", `{"action":"audio","value":0}`); code != 200 || dev.count("LOAD") != 1 {
		t.Fatalf("choosing the current track must not reload: %d %v, %d loads", code, body, dev.count("LOAD"))
	}
	if code, body := call(t, m, "POST", "/api/cast/dev1/control", `{"action":"audio","value":1}`); code != 200 {
		t.Fatalf("audio switch = %d %v", code, body)
	}
	load := dev.last("LOAD")
	media := load["media"].(map[string]any)
	if got := media["contentId"]; got != "http://192.168.1.10:8200/hls/abc123/1/index.m3u8" {
		t.Errorf("contentId after the switch = %v", got)
	}
	if got := load["currentTime"].(float64); got < 40 {
		t.Errorf("the switch must keep the position; currentTime = %v", got)
	}
	if code, _ := call(t, m, "POST", "/api/cast/dev1/control", `{"action":"audio","value":"two"}`); code != 400 {
		t.Errorf("audio with a non-number = %d, want 400", code)
	}
}

func TestTitleFor(t *testing.T) {
	movie := model.Parsed{Type: model.TypeMovie, Title: "Parsed Name"}
	episode := model.Parsed{Type: model.TypeEpisode, Show: "Show", Season: 2, Episode: 5}
	tests := []struct {
		name string
		item model.Item
		want string
	}{
		{"movie with metadata", model.Item{Parsed: movie, Meta: &model.Meta{Title: "Real Title"}}, "Real Title"},
		{"movie without metadata", model.Item{Parsed: movie}, "Parsed Name"},
		{"movie with an empty title", model.Item{Parsed: movie, Meta: &model.Meta{}}, "Parsed Name"},
		{"episode with metadata", model.Item{Parsed: episode, Meta: &model.Meta{Title: "Pilot"}}, "Show S02E05 Pilot"},
		{"episode without metadata", model.Item{Parsed: episode}, "Show S02E05"},
	}
	for _, tt := range tests {
		if got := titleFor(tt.item); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
