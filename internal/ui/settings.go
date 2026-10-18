package ui

import (
	"context"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/client"
	"homemedia/internal/ui/vm"
)

const tmdbKeysURL = "https://www.themoviedb.org/settings/api"

// settingsScreen is the Settings tab: the TMDB key and language, and the media
// folder. The server decides who may save; a remote app sees the form read-only.
// Fields are touched only on the UI thread.
type settingsScreen struct {
	a    *App
	cur  client.Settings
	busy bool

	mediaDir  *widget.Entry
	choose    *widget.Button
	language  *widget.Select
	key       *widget.Entry
	removeKey *widget.Check
	save      *widget.Button
	notes     *widget.Label
	status    *widget.Label
	body      fyne.CanvasObject
}

// NewSettings is the Settings tab. Call load when the tab is shown.
func NewSettings(a *App) *settingsScreen {
	s := &settingsScreen{a: a}
	s.mediaDir = widget.NewEntry()
	s.mediaDir.SetPlaceHolder("/Users/you/Movies")
	s.choose = widget.NewButton("Choose…", s.pickFolder)
	s.language = widget.NewSelect(vm.LanguageOptions(""), nil)
	s.key = widget.NewPasswordEntry()
	s.removeKey = widget.NewCheck("Remove the saved key", nil)
	s.save = widget.NewButton("Save", s.submit)
	s.save.Importance = widget.HighImportance
	s.notes = widget.NewLabel("")
	s.notes.Wrapping = fyne.TextWrapWord
	s.status = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapWord

	link, _ := url.Parse(tmdbKeysURL)
	form := widget.NewForm(
		widget.NewFormItem("Media folder", container.NewBorder(nil, nil, nil, s.choose, s.mediaDir)),
		widget.NewFormItem("Metadata language", s.language),
		widget.NewFormItem("TMDB key", container.NewVBox(s.key, container.NewHBox(s.removeKey, widget.NewHyperlink("Get a free key", link)))),
	)
	s.body = container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		form, s.save, s.notes, s.status,
	)))
	return s
}

// Object is the tab content.
func (s *settingsScreen) Object() fyne.CanvasObject { return s.body }

// load fetches the server's settings and fills the form.
func (s *settingsScreen) load() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cur, err := s.a.Client.Settings(ctx)
		fyne.Do(func() {
			if err != nil {
				s.status.SetText("Can't load settings: " + err.Error())
				return
			}
			s.render(cur)
		})
	}()
}

// render shows cur and locks what this caller may not change.
func (s *settingsScreen) render(cur client.Settings) {
	s.cur = cur
	s.mediaDir.SetText(cur.MediaDir)
	s.language.SetOptions(vm.LanguageOptions(cur.TMDBLanguage))
	s.language.SetSelected(cur.TMDBLanguage)
	s.key.SetText("")
	s.key.SetPlaceHolder(vm.KeyPlaceholder(cur.TMDBKeySet))
	s.removeKey.SetChecked(false)

	set := func(w fyne.Disableable, locked bool) {
		if cur.Editable && !locked {
			w.Enable()
		} else {
			w.Disable()
		}
	}
	dirLocked := vm.SettingLocked(cur, vm.SettingMediaDir)
	keyLocked := vm.SettingLocked(cur, vm.SettingTMDBKey)
	set(s.mediaDir, dirLocked)
	set(s.choose, dirLocked)
	set(s.language, vm.SettingLocked(cur, vm.SettingTMDBLang))
	set(s.key, keyLocked)
	set(s.removeKey, keyLocked || !cur.TMDBKeySet)
	set(s.save, false)

	text := ""
	for i, n := range vm.SettingsNotes(cur) {
		if i > 0 {
			text += "\n\n"
		}
		text += n
	}
	s.notes.SetText(text)
}

func (s *settingsScreen) pickFolder() {
	d := dialog.NewFolderOpen(func(dir fyne.ListableURI, err error) {
		if err == nil && dir != nil {
			s.mediaDir.SetText(dir.Path())
		}
	}, s.a.Win)
	d.Show()
}

// submit saves what changed.
func (s *settingsScreen) submit() {
	if s.busy {
		return
	}
	u, changed := vm.BuildSettingsUpdate(s.cur, s.mediaDir.Text, s.language.Selected, s.key.Text, s.removeKey.Checked)
	if !changed {
		s.status.SetText("Nothing to save.")
		return
	}
	s.busy = true
	s.save.Disable()
	s.status.SetText("Saving…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cur, err := s.a.Client.SaveSettings(ctx, u)
		fyne.Do(func() {
			s.busy = false
			if err != nil {
				s.save.Enable()
				s.status.SetText("")
				dialog.ShowError(err, s.a.Win)
				return
			}
			s.render(cur)
			s.status.SetText("Saved.")
			if u.TMDBKey != nil || u.TMDBLanguage != nil {
				s.a.Reload() // new artwork arrives as the server rescans
			}
		})
	}()
}
