package metadata

import "testing"

func TestEnabled(t *testing.T) {
	if (&Client{}).Enabled() {
		t.Error("empty key must be disabled")
	}
	if !(&Client{APIKey: "abc"}).Enabled() {
		t.Error("key set must be enabled")
	}
}
