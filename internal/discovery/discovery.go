// Package discovery advertises this server and finds other servers via mDNS.
package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// Service is the mDNS service type for this server.
const Service = "_homemedia._tcp"

// Server is a discovered mediad instance.
type Server struct {
	Name string
	Host string
	Port int
	URL  string // http://<ip>:<port>
}

// Advertise registers a `_homemedia._tcp` service (instance name = name,
// TXT: version=1) until ctx is cancelled.
func Advertise(ctx context.Context, name string, port int) error {
	return advertise(ctx, name, port, nil)
}

// advertise is Advertise on the given interfaces (nil means every multicast interface).
func advertise(ctx context.Context, name string, port int, ifaces []net.Interface) error {
	srv, err := zeroconf.Register(name, Service, "local.", port, []string{"version=1"}, ifaces)
	if err != nil {
		return err
	}
	<-ctx.Done()
	srv.Shutdown()
	return nil
}

// Browse returns servers found within timeout (ctx also cancels).
func Browse(ctx context.Context, timeoutSeconds int) ([]Server, error) {
	return browse(ctx, timeoutSeconds, nil)
}

// browse is Browse on the given interfaces (nil means every multicast interface).
func browse(ctx context.Context, timeoutSeconds int, ifaces []net.Interface) ([]Server, error) {
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIfaces(ifaces))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	entries := make(chan *zeroconf.ServiceEntry)
	browseErr := make(chan error, 1)
	go func() { browseErr <- resolver.Browse(ctx, Service, "local.", entries) }()

	seen := make(map[string]bool)
	var found []Server
collect:
	for {
		select {
		case e, ok := <-entries:
			if !ok {
				break collect
			}
			if seen[e.Instance] {
				continue
			}
			seen[e.Instance] = true
			found = append(found, serverFromEntry(e))
		case <-ctx.Done():
			break collect
		}
	}

	// Wait for Browse to return so nothing is still sending on entries.
	for {
		select {
		case <-entries:
		case err := <-browseErr:
			if err != nil && ctx.Err() == nil {
				return found, err
			}
			return found, nil
		}
	}
}

// serverFromEntry converts a browse result into a Server, preferring the first
// IPv4 address and falling back to the advertised hostname.
func serverFromEntry(e *zeroconf.ServiceEntry) Server {
	var host string
	switch {
	case len(e.AddrIPv4) > 0:
		host = e.AddrIPv4[0].String()
	case strings.TrimSuffix(e.HostName, ".") != "":
		host = strings.TrimSuffix(e.HostName, ".")
	default:
		host, _ = os.Hostname()
	}
	return Server{
		Name: e.Instance,
		Host: host,
		Port: e.Port,
		URL:  fmt.Sprintf("http://%s:%d", host, e.Port),
	}
}
