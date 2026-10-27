package ui

import (
	"context"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"homemedia/internal/ui/vm"
)

const qrSize = 280

// phoneScreen is the Phone tab: the address of the phone page and a QR code for
// it. Fields are touched only on the UI thread.
type phoneScreen struct {
	a    *App
	qr   *canvas.Image
	url  *widget.Label
	note *widget.Label
	copy *widget.Button
	body fyne.CanvasObject
}

// NewPhone is the Phone tab. Call load when the tab is shown: the LAN address
// can change when the Mac switches Wi-Fi.
func NewPhone(a *App) *phoneScreen {
	p := &phoneScreen{a: a}
	p.qr = canvas.NewImageFromResource(nil)
	p.qr.FillMode = canvas.ImageFillContain
	p.qr.SetMinSize(fyne.NewSize(qrSize, qrSize))
	p.url = widget.NewLabel("")
	p.url.Alignment = fyne.TextAlignCenter
	p.url.TextStyle = fyne.TextStyle{Bold: true}
	p.note = widget.NewLabel("")
	p.note.Alignment = fyne.TextAlignCenter
	p.note.Wrapping = fyne.TextWrapWord
	p.copy = widget.NewButtonWithIcon("Copy address", theme.ContentCopyIcon(), func() {
		a.Fyne.Clipboard().SetContent(p.url.Text)
	})
	p.copy.Hide()
	p.body = container.NewCenter(container.NewVBox(
		widget.NewLabelWithStyle("Control from your phone", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		p.qr, p.url, p.note, container.NewCenter(p.copy),
	))
	return p
}

// Object is the tab content.
func (p *phoneScreen) Object() fyne.CanvasObject { return p.body }

// load fetches the server's address and redraws.
func (p *phoneScreen) load() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := p.a.Client.Info(ctx)
		var png []byte
		var url string
		ok := false
		if err == nil {
			url, ok = vm.PhoneURL(info.Base)
			if ok {
				png, err = vm.QRPNG(url, qrSize*2) // 2x so it stays sharp on Retina
			}
		}
		fyne.Do(func() {
			switch {
			case err != nil:
				p.show("", nil, "Can't reach the server: "+err.Error())
			case !ok:
				p.show("", nil, "The server only knows a local address, so a phone can't reach it. Connect this Mac to Wi-Fi and reopen this tab.")
			default:
				p.show(url, png, "Scan with your phone's camera while it's on the same Wi-Fi. Then use Share → Add to Home Screen.")
			}
		})
	}()
}

func (p *phoneScreen) show(url string, png []byte, note string) {
	p.url.SetText(url)
	p.note.SetText(note)
	if png == nil {
		p.qr.Resource = nil
		p.qr.Hide()
		p.copy.Hide()
	} else {
		p.qr.Resource = fyne.NewStaticResource("phone-qr.png", png)
		p.qr.Show()
		p.copy.Show()
	}
	p.qr.Refresh()
}
