package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"homemedia/internal/client"
)

// The client keeps its own copies of the settings types; a field or tag that
// drifts from the server's would silently drop data on the wire.
func TestSettingsWireMatchesClient(t *testing.T) {
	s := "x"
	for _, tc := range []struct {
		name     string
		server   any
		clientTo any
	}{
		{"view", SettingsView{MediaDir: "/m", TMDBLanguage: "es-AR", TMDBKeySet: true, Port: 8200, Overridden: []string{"PORT"}, RestartRequired: true, Editable: true}, &client.Settings{}},
		{"update", SettingsUpdate{MediaDir: &s, TMDBLanguage: &s, TMDBKey: &s}, &client.SettingsUpdate{}},
	} {
		b, err := json.Marshal(tc.server)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, tc.clientTo); err != nil {
			t.Fatal(err)
		}
		// Every server field must arrive non-zero, and the client must have no extra fields.
		got := reflect.ValueOf(tc.clientTo).Elem()
		want := reflect.TypeOf(tc.server)
		if got.NumField() != want.NumField() {
			t.Errorf("%s: client has %d fields, server %d", tc.name, got.NumField(), want.NumField())
		}
		for i := range got.NumField() {
			if got.Field(i).IsZero() {
				t.Errorf("%s: %s did not survive the round trip", tc.name, got.Type().Field(i).Name)
			}
		}
		back, _ := json.Marshal(tc.clientTo)
		if string(back) != string(b) {
			t.Errorf("%s: client re-encodes to %s, server sent %s", tc.name, back, b)
		}
	}
}
