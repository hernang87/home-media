package cast

import (
	"errors"
	"sync"
	"testing"
	"time"

	"homemedia/internal/model"
)

func TestConcurrentEnsureDialsOnce(t *testing.T) {
	m, dev, _ := setup(t, testItem("a.mp4", h264, aac))
	s := m.sessionFor(Device{ID: "dev1", Name: "Living Room", Model: "Chromecast", Addr: "127.0.0.1", Port: dev.Port})

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ensure(ctx(t)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := dev.accepted.Load(); n != 1 {
		t.Errorf("device accepted %d connections, want 1", n)
	}
}

func TestSetBaseChangesLoadURLs(t *testing.T) {
	m := NewManager(&fakeLib{}, "http://192.168.1.10:8200")
	m.SetBase("http://10.0.0.5:8200/")
	spec, err := m.buildLoad(testItem("a.mp4", h264, aac), "Chromecast", PlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	media := spec.payload["media"].(map[string]any)
	if media["contentId"] != "http://10.0.0.5:8200/file/abc123" {
		t.Errorf("contentId = %v", media["contentId"])
	}
}

func TestDropDuringStartupFailsFast(t *testing.T) {
	for _, step := range []string{"GET_STATUS", "LAUNCH", "LOAD"} {
		t.Run(step, func(t *testing.T) {
			m, dev, _ := setup(t, testItem("a.mp4", h264, aac))
			dev.hangUpOn(step)
			start := time.Now()
			err := m.Play(ctx(t), "dev1", PlayRequest{ItemID: "abc123"})
			if !errors.Is(err, errClosed) {
				t.Fatalf("err = %v, want errClosed", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Play took %v after the device hung up", d)
			}
			if st, _ := m.Status("dev1"); st.State != model.StateDisconnected {
				t.Errorf("state = %s, want DISCONNECTED", st.State)
			}
		})
	}
}
