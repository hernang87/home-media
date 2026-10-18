package cast

import (
	"sync"
	"testing"
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
	spec, err := m.BuildLoad(testItem("a.mp4", h264, aac), "Chromecast", PlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	media := spec.payload["media"].(map[string]any)
	if media["contentId"] != "http://10.0.0.5:8200/file/abc123" {
		t.Errorf("contentId = %v", media["contentId"])
	}
}
