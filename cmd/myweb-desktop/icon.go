package main

import (
	_ "embed"
	"runtime"

	"fyne.io/systray"
)

// The tray icons, made from the file manager's icon: in colour while the
// server runs, grey while it doesn't. macOS draws the template ones in the
// menu bar's own colour.
var (
	//go:embed icons/on.png
	iconOn []byte
	//go:embed icons/off.png
	iconOff []byte
	//go:embed icons/on.ico
	icoOn []byte
	//go:embed icons/off.ico
	icoOff []byte
	//go:embed icons/on-template.png
	templateOn []byte
	//go:embed icons/off-template.png
	templateOff []byte
)

func setTrayIcon(on bool) {
	png, ico, tmpl := iconOff, icoOff, templateOff
	if on {
		png, ico, tmpl = iconOn, icoOn, templateOn
	}
	switch runtime.GOOS {
	case "windows":
		systray.SetIcon(ico)
	case "darwin":
		systray.SetTemplateIcon(tmpl, png)
	default:
		systray.SetIcon(png)
	}
}
