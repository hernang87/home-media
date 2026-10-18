package cast

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"

	"homemedia/internal/model"
)

type fakeLib struct {
	mu    sync.Mutex
	item  model.Item
	saved [][3]any // id, pos, dur

	prog    model.Progress // returned by Progress when hasProg
	hasProg bool
}

func (l *fakeLib) Get(id string) (model.Item, bool) { return l.item, id == l.item.ID }
func (l *fakeLib) SetProgress(id string, pos, dur float64) {
	l.mu.Lock()
	l.saved = append(l.saved, [3]any{id, pos, dur})
	l.mu.Unlock()
}
func (l *fakeLib) Progress(id string) (model.Progress, bool) { return l.prog, l.hasProg }
func (l *fakeLib) lastSaved() ([3]any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.saved) == 0 {
		return [3]any{}, false
	}
	return l.saved[len(l.saved)-1], true
}

func testItem(path string, v model.VideoInfo, audio ...model.AudioTrack) model.Item {
	return model.Item{
		ID: "abc123", Path: path, Rel: "Movie.mp4",
		Parsed: model.Parsed{Type: model.TypeMovie, Title: "Movie"},
		Info:   &model.MediaInfo{Duration: 100, Video: &v, Audio: audio},
		Subs: []model.SubTrack{
			{ID: "e0", Source: "external", Lang: "eng", Label: "English"},
			{ID: "i0", Source: "embedded", Lang: "spa", Label: "Spanish"},
		},
	}
}

var h264 = model.VideoInfo{Codec: "h264", PixFmt: "yuv420p", Level: 41, Height: 1080}
var aac = model.AudioTrack{Codec: "aac", Channels: 2}

func setup(t *testing.T, item model.Item) (*Manager, *fakeDevice, *fakeLib) {
	t.Helper()
	dev := newFakeDevice(t)
	lib := &fakeLib{item: item}
	m := NewManager(lib, "http://192.168.1.10:8200")
	m.AddDevice(Device{ID: "dev1", Name: "Living Room", Model: "Chromecast", Addr: "127.0.0.1", Port: dev.Port})
	return m, dev, lib
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestBuildLoadDirectAndHLS(t *testing.T) {
	m := NewManager(&fakeLib{}, "http://192.168.1.10:8200/")
	direct, err := m.BuildLoad(testItem("a.mp4", h264, aac), "Chromecast", PlayRequest{Sub: "e0", Start: 12})
	if err != nil {
		t.Fatal(err)
	}
	media := direct.payload["media"].(map[string]any)
	if media["contentId"] != "http://192.168.1.10:8200/file/abc123" || media["contentType"] != "video/mp4" {
		t.Errorf("direct media = %v", media)
	}
	if got := direct.payload["activeTrackIds"].([]int); len(got) != 1 || got[0] != 1 {
		t.Errorf("activeTrackIds = %v", got)
	}
	tracks := media["tracks"].([]map[string]any)
	if len(tracks) != 2 || tracks[0]["trackContentId"] != "http://192.168.1.10:8200/sub/abc123/e0.vtt" || tracks[0]["trackContentType"] != "text/vtt" {
		t.Errorf("tracks = %v", tracks)
	}
	if direct.payload["currentTime"] != 12.0 {
		t.Errorf("start = %v", direct.payload["currentTime"])
	}

	hls, err := m.BuildLoad(testItem("a.mkv", h264, aac, aac), "Chromecast", PlayRequest{Audio: 1})
	if err != nil {
		t.Fatal(err)
	}
	media = hls.payload["media"].(map[string]any)
	if media["contentId"] != "http://192.168.1.10:8200/hls/abc123/1/index.m3u8" || media["contentType"] != "application/x-mpegURL" || hls.mode != "transcode" {
		t.Errorf("hls media = %v", media)
	}
	if _, err := m.BuildLoad(testItem("a.mkv", h264, aac), "Chromecast", PlayRequest{Mode: "direct"}); err == nil {
		t.Error("forcing direct on an mkv must fail")
	}
	if _, err := m.BuildLoad(testItem("a.mp4", h264, aac), "Chromecast", PlayRequest{Sub: "nope"}); err == nil {
		t.Error("unknown subtitle must fail")
	}
}

func TestPlayControlAndProgress(t *testing.T) {
	m, dev, lib := setup(t, testItem("a.mp4", h264, aac))
	stopped := make(chan string, 1)
	m.StopHook = func(id string) { stopped <- id }

	if err := m.Play(ctx(t), "Living Room", PlayRequest{ItemID: "abc123", Sub: "e0", Start: 30}); err != nil { // by name
		t.Fatal(err)
	}
	load := dev.last("LOAD")
	if load == nil || load["_ns"] != nsMedia {
		t.Fatalf("no LOAD received: %v", load)
	}
	if ids := load["activeTrackIds"].([]any); len(ids) != 1 || ids[0].(float64) != 1 {
		t.Errorf("LOAD activeTrackIds = %v", ids)
	}
	if dev.last("LAUNCH")["appId"] != defaultReceiverApp {
		t.Error("expected the Default Media Receiver to be launched")
	}
	st, _ := m.Status("dev1")
	if st.State != "PLAYING" || st.ItemID != "abc123" || st.Sub != "e0" || st.Duration != 100 || st.Position < 30 {
		t.Errorf("status after play = %+v", st)
	}

	// Switch subtitles to Spanish, then off.
	control := func(action string, value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := m.Control(ctx(t), "dev1", ControlRequest{Action: action, Value: raw}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	control("sub", "i0")
	waitFor(t, func() bool { s, _ := m.Status("dev1"); return s.Sub == "i0" })
	control("sub", "")
	waitFor(t, func() bool { s, _ := m.Status("dev1"); return s.Sub == "" })
	if ids := dev.last("EDIT_TRACKS_INFO")["activeTrackIds"].([]any); len(ids) != 0 {
		t.Errorf("EDIT_TRACKS_INFO ids = %v, want empty", ids)
	}

	control("seek", 55.0)
	waitFor(t, func() bool { s, _ := m.Status("dev1"); return s.Position >= 55 })

	// Pausing stores progress.
	control("pause", nil)
	waitFor(t, func() bool { s, _ := m.Status("dev1"); return s.State == "PAUSED" })
	waitFor(t, func() bool { v, ok := lib.lastSaved(); return ok && v[1].(float64) >= 55 })

	control("volume", 0.3)
	waitFor(t, func() bool { v := dev.last("SET_VOLUME"); return v != nil && v["_ns"] == nsReceiver })
	if err := m.Control(ctx(t), "dev1", ControlRequest{Action: "volume", Value: json.RawMessage("2")}); err == nil {
		t.Error("volume 2 must be rejected")
	}
	if err := m.Control(ctx(t), "dev1", ControlRequest{Action: "sub", Value: json.RawMessage(`"zz"`)}); err == nil {
		t.Error("unknown subtitle must be rejected")
	}

	// End of media: progress becomes "watched" and transcoding is told to stop.
	control("resume", nil)
	dev.finish()
	select {
	case id := <-stopped:
		if id != "abc123" {
			t.Errorf("stop hook id = %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop hook not called when media finished")
	}
	// The stop hook fires under the session lock; the save follows after it.
	waitFor(t, func() bool { v, ok := lib.lastSaved(); return ok && v[1].(float64) == 100 })
}

func TestStopClosesReceiverApp(t *testing.T) {
	m, dev, _ := setup(t, testItem("a.mp4", h264, aac))
	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Control(ctx(t), "dev1", ControlRequest{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s, _ := m.Status("dev1"); return s.State == "IDLE" })
	if dev.last("STOP")["sessionId"] != "s-1" {
		t.Errorf("STOP = %v", dev.last("STOP"))
	}
}

func TestUnknownDeviceAndItem(t *testing.T) {
	m, _, _ := setup(t, testItem("a.mp4", h264, aac))
	if _, err := m.Status("nope"); err == nil {
		t.Error("unknown device must fail")
	}
	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "missing"}); err == nil {
		t.Error("unknown item must fail")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func TestPlayResumeChoice(t *testing.T) {
	m, dev, lib := setup(t, testItem("a.mp4", h264, aac))
	lib.prog, lib.hasProg = model.Progress{Pos: 1500, Dur: 7200}, true

	var re *ResumeError
	err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123"})
	if !errors.As(err, &re) || re.Position != 1500 || re.Duration != 7200 {
		t.Fatalf("want ResumeError at 1500, got %v", err)
	}
	if dev.last("LOAD") != nil {
		t.Fatal("LOAD sent before the user chose")
	}

	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123", Resume: "continue"}); err != nil {
		t.Fatal(err)
	}
	if got := dev.last("LOAD")["currentTime"]; got != 1500.0 {
		t.Fatalf("continue: currentTime = %v, want 1500", got)
	}

	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123", Resume: "restart"}); err != nil {
		t.Fatal(err)
	}
	if got := dev.last("LOAD")["currentTime"]; got != 0.0 {
		t.Fatalf("restart: currentTime = %v, want 0", got)
	}

	// Watched, tiny and explicit-start cases never ask.
	for _, tc := range []struct {
		name string
		prog model.Progress
		req  PlayRequest
	}{
		{"watched", model.Progress{Pos: 1500, Watched: true}, PlayRequest{ItemID: "abc123"}},
		{"tiny", model.Progress{Pos: 10}, PlayRequest{ItemID: "abc123"}},
		{"explicit start", model.Progress{Pos: 1500}, PlayRequest{ItemID: "abc123", Start: 42}},
	} {
		lib.prog = tc.prog
		if err := m.Play(ctx(t), "dev1", tc.req); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123", Resume: "bogus"}); !errors.Is(err, errBadRequest) {
		t.Fatalf("bogus resume: %v", err)
	}
}

func TestStopSavesPosition(t *testing.T) {
	m, _, lib := setup(t, testItem("a.mp4", h264, aac))
	if err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123", Start: 45}); err != nil {
		t.Fatal(err)
	}
	if err := m.Control(ctx(t), "dev1", ControlRequest{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	got, ok := lib.lastSaved()
	if !ok || got[0] != "abc123" || got[1].(float64) < 45 {
		t.Fatalf("stop did not save the position, got %v (saved=%v)", got, ok)
	}
}

func TestDeviceFromEntrySkipsAudioOnly(t *testing.T) {
	entry := func(ca string) *zeroconf.ServiceEntry {
		e := zeroconf.NewServiceEntry("x", "_googlecast._tcp", "local.")
		e.AddrIPv4 = []net.IP{net.ParseIP("192.168.1.20")}
		e.Port = 8009
		e.Text = []string{"id=abc", "fn=Thing", "md=Chromecast"}
		if ca != "" {
			e.Text = append(e.Text, "ca="+ca)
		}
		return e
	}
	for _, tc := range []struct {
		ca   string
		want bool
	}{{"", true}, {"5", true}, {"199173", true}, {"4", false}, {"2052", false}} {
		if _, ok := deviceFromEntry(entry(tc.ca)); ok != tc.want {
			t.Errorf("ca=%q: shown=%v, want %v", tc.ca, ok, tc.want)
		}
	}
}
