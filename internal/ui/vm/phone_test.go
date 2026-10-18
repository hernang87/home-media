package vm

import (
	"bytes"
	"image/png"
	"testing"
)

func TestPhoneURL(t *testing.T) {
	tests := []struct {
		base   string
		wantOK bool
	}{
		{"http://192.168.1.10:8200", true},
		{"http://mac.local:8200", true},
		{"http://127.0.0.1:8200", false},
		{"http://localhost:8200", false},
		{"http://[::1]:8200", false},
		{"http://0.0.0.0:8200", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			url, ok := PhoneURL(tt.base)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if url != tt.base {
				t.Errorf("url = %q, want %q", url, tt.base)
			}
		})
	}
}

func TestQRPNG(t *testing.T) {
	b, err := QRPNG("http://192.168.1.10:8200", 256)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if got := img.Bounds().Dx(); got != 256 {
		t.Errorf("width = %d, want 256", got)
	}
}
