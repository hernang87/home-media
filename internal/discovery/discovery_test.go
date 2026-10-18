package discovery

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/grandcat/zeroconf"
)

func TestCollect(t *testing.T) {
	entry := func(name string, port int) *zeroconf.ServiceEntry {
		return &zeroconf.ServiceEntry{
			ServiceRecord: zeroconf.ServiceRecord{Instance: name, Service: Service, Domain: "local."},
			HostName:      name + ".local.",
			Port:          port,
			AddrIPv4:      []net.IP{net.ParseIP("192.168.1.20")},
		}
	}
	tests := []struct {
		name   string
		feed   []*zeroconf.ServiceEntry
		err    error
		want   []Server
		wantEr bool
	}{
		{name: "none found"},
		{
			name: "dedupes by instance",
			feed: []*zeroconf.ServiceEntry{entry("a", 8200), entry("a", 8200), entry("b", 8201)},
			want: []Server{
				{Name: "a", Host: "192.168.1.20", Port: 8200, URL: "http://192.168.1.20:8200"},
				{Name: "b", Host: "192.168.1.20", Port: 8201, URL: "http://192.168.1.20:8201"},
			},
		},
		{
			name:   "browse error is returned with partial results",
			feed:   []*zeroconf.ServiceEntry{entry("a", 8200)},
			err:    errors.New("boom"),
			want:   []Server{{Name: "a", Host: "192.168.1.20", Port: 8200, URL: "http://192.168.1.20:8200"}},
			wantEr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			browse := func(ctx context.Context, out chan<- *zeroconf.ServiceEntry) error {
				for _, e := range tc.feed {
					out <- e
				}
				if tc.err == nil {
					close(out) // a real browse closes entries when its context ends
				}
				return tc.err
			}
			got, err := collect(context.Background(), 5, browse)
			if (err != nil) != tc.wantEr {
				t.Fatalf("err = %v, want error: %v", err, tc.wantEr)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("collect() = %+v, want %+v", got, tc.want)
			}
		})
	}
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
