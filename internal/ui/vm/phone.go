package vm

import (
	"net"

	qrcode "github.com/skip2/go-qrcode"
)

// PhoneURL is the address to open on a phone, from the server's base URL
// (http://<lan>:<port>). ok is false when the server only knows a loopback
// address: a phone cannot reach that.
func PhoneURL(base string) (url string, ok bool) {
	host := base
	if i := indexAfterScheme(base); i >= 0 {
		host = base[i:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return base, false
	}
	return base, true
}

func indexAfterScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i + 3
		}
	}
	return -1
}

// QRPNG renders text as a QR code PNG, size pixels square.
func QRPNG(text string, size int) ([]byte, error) {
	return qrcode.Encode(text, qrcode.Medium, size)
}
