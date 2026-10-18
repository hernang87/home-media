package config

import (
	"net"
	"testing"
)

func TestLANAddressIsIPv4(t *testing.T) {
	ip := net.ParseIP(LANAddress())
	if ip == nil || ip.To4() == nil {
		t.Fatalf("LANAddress() = %v, want an IPv4 address", ip)
	}
}

func TestLoadUsesMediaArg(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaDir != dir || c.Port != 8200 || c.ServerName != "Home Media" {
		t.Errorf("unexpected defaults: %+v", c)
	}
}
