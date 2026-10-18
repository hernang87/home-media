package vm

import (
	"testing"

	"homemedia/internal/model"
)

func TestTrayStatus(t *testing.T) {
	tests := []struct {
		name  string
		st    model.CastStatus
		title string
		want  Tray
	}{
		{"idle", model.CastStatus{State: "IDLE"}, "", Tray{Label: "Nothing playing", ToggleText: "Pause"}},
		{"disconnected", model.CastStatus{State: "DISCONNECTED"}, "X", Tray{Label: "Nothing playing", ToggleText: "Pause"}},
		{"playing", model.CastStatus{State: "PLAYING"}, "Alien", Tray{Label: "Alien · Playing", Active: true, Playing: true, ToggleText: "Pause"}},
		{"paused", model.CastStatus{State: "PAUSED"}, "Alien", Tray{Label: "Alien · Paused", Active: true, ToggleText: "Resume"}},
		{"buffering", model.CastStatus{State: "BUFFERING"}, "Alien", Tray{Label: "Alien · Loading", Active: true, ToggleText: "Resume"}},
		{"unknown title", model.CastStatus{State: "PLAYING"}, "", Tray{Label: "Unknown title · Playing", Active: true, Playing: true, ToggleText: "Pause"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrayStatus(tc.st, tc.title); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAllWatched(t *testing.T) {
	w := movie("w", "W", 1, &model.PublicProgress{Watched: true})
	u := movie("u", "U", 2, nil)
	tests := []struct {
		name  string
		items []model.PublicItem
		want  bool
	}{
		{"empty", nil, false},
		{"all", []model.PublicItem{w, w}, true},
		{"mixed", []model.PublicItem{w, u}, false},
	}
	for _, tc := range tests {
		if got := AllWatched(tc.items); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := IDs([]model.PublicItem{w, u}); len(got) != 2 || got[0] != "w" || got[1] != "u" {
		t.Errorf("IDs = %v", got)
	}
}

func TestToggleAction(t *testing.T) {
	if got := (Tray{Playing: true}).ToggleAction(); got != "pause" {
		t.Errorf("playing: %q", got)
	}
	if got := (Tray{}).ToggleAction(); got != "resume" {
		t.Errorf("paused: %q", got)
	}
}
