//go:build mdns

// Real multicast round trip. CI runners drop mDNS between sockets on one host,
// so run it by hand on a Mac with a LAN: go test -tags mdns ./internal/discovery

package discovery

import (
	"context"
	"testing"
	"time"
)

func TestAdvertiseThenBrowse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	advErr := make(chan error, 1)
	go func() { advErr <- Advertise(ctx, "test-server", 18200) }()
	time.Sleep(time.Second)
	select {
	case err := <-advErr:
		if err != nil {
			t.Skipf("cannot register mDNS service (sandbox may block multicast): %v", err)
		}
	default:
	}
	found, err := Browse(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range found {
		if s.Name == "test-server" && s.Port == 18200 {
			return
		}
	}
	t.Fatalf("test-server not found among %+v", found)
}
