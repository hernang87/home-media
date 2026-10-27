package vm

import (
	"homemedia/internal/model"
)

// Tray is what the menu bar item shows for one TV's status.
type Tray struct {
	Label      string // "Title · Playing"; "Nothing playing" when idle
	Active     bool   // something is loaded, so Pause/Resume and Stop make sense
	Playing    bool   // true: the toggle pauses; false: it resumes
	ToggleText string // "Pause" or "Resume"
}

// ToggleAction is the control command the Pause/Resume item sends.
func (t Tray) ToggleAction() string {
	if t.Playing {
		return "pause"
	}
	return "resume"
}

// TrayStatus summarizes st for the menu bar. title is the playing item's title
// ("" when unknown).
func TrayStatus(st model.CastStatus, title string) Tray {
	if !st.State.Active() && st.State != model.StateLoading {
		return Tray{Label: "Nothing playing", ToggleText: "Pause"}
	}
	if title == "" {
		title = "Unknown title"
	}
	t := Tray{Active: true, Playing: st.State == model.StatePlaying, ToggleText: "Resume"}
	state := "Paused"
	switch st.State {
	case model.StatePlaying:
		state, t.ToggleText = "Playing", "Pause"
	case model.StateBuffering, model.StateLoading:
		state = "Loading"
	}
	t.Label = title + " · " + state
	return t
}

// AllWatched reports whether every item has been watched. An empty list counts
// as not watched, so a season with no episodes offers "mark watched".
func AllWatched(items []model.PublicItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if it.Progress == nil || !it.Progress.Watched {
			return false
		}
	}
	return true
}

// IDs lists the item IDs in order.
func IDs(items []model.PublicItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}
