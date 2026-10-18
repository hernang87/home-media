package discovery

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

func loopback(t *testing.T) []net.Interface {
	t.Helper()
	all, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range all {
		if i.Flags&net.FlagLoopback != 0 && i.Flags&net.FlagUp != 0 && i.Flags&net.FlagMulticast != 0 {
			return []net.Interface{i}
		}
	}
	t.Fatalf("no multicast-capable loopback interface among %+v", all)
	return nil
}

func TestAdvertiseThenBrowse(t *testing.T) {
	// Loopback keeps the test independent of the host's network: CI runners
	// have no LAN to multicast on.
	lo := loopback(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	advErr := make(chan error, 1)
	go func() { advErr <- advertise(ctx, "test-server", 18200, lo) }()
	time.Sleep(time.Second)
	select {
	case err := <-advErr:
		if err != nil {
			t.Skipf("cannot register mDNS service (sandbox may block multicast): %v", err)
		}
	default:
	}
	found, err := browse(ctx, 3, lo)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range found {
		if s.Name == "test-server" && s.Port == 18200 {
			return
		}
	}
	all, _ := net.Interfaces()
	for _, i := range all {
		addrs, _ := i.Addrs()
		t.Logf("iface %s flags=%v addrs=%v", i.Name, i.Flags, addrs)
	}
	t.Fatalf("test-server not found among %+v (using %s)", found, lo[0].Name)
}

func TestServerFromEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry zeroconf.ServiceEntry
		want  Server
	}{
		{
			name: "prefers first IPv4",
			entry: zeroconf.ServiceEntry{
				ServiceRecord: zeroconf.ServiceRecord{Instance: "box", Service: Service, Domain: "local."},
				HostName:      "box.local.",
				Port:          8200,
				AddrIPv4:      []net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("192.168.1.21")},
				AddrIPv6:      []net.IP{net.ParseIP("fe80::1")},
			},
			want: Server{Name: "box", Host: "192.168.1.20", Port: 8200, URL: "http://192.168.1.20:8200"},
		},
		{
			name: "falls back to hostname without trailing dot",
			entry: zeroconf.ServiceEntry{
				ServiceRecord: zeroconf.ServiceRecord{Instance: "box", Service: Service, Domain: "local."},
				HostName:      "box.local.",
				Port:          8200,
				AddrIPv6:      []net.IP{net.ParseIP("fe80::1")},
			},
			want: Server{Name: "box", Host: "box.local", Port: 8200, URL: "http://box.local:8200"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := serverFromEntry(&tc.entry)
			if got != tc.want {
				t.Fatalf("serverFromEntry() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
