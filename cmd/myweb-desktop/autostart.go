package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Start at login: a launchd agent on macOS, an XDG autostart entry on Linux,
// the Run key in the registry on Windows (autostart_windows.go). It starts
// this program with autostartArg, which serves without opening the browser.

const autostartArg = "-autostart"

const label = "com.github.nimezhu.myweb-desktop"

// autostartFile is where the entry is on macOS and Linux.
func autostartFile(goos string) (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(h, "Library", "LaunchAgents", label+".plist"), nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "autostart", "myweb-desktop.desktop"), nil
}

// autostartEntry is the file's content for the program at exe.
func autostartEntry(goos, exe string) string {
	if goos == "darwin" {
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
		<string>` + autostartArg + `</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`
	}
	return `[Desktop Entry]
Type=Application
Name=myweb
Comment=Serve your data folder to web apps, from the system tray
Exec=` + desktopQuote(exe) + ` ` + autostartArg + `
Terminal=false
X-GNOME-Autostart-enabled=true
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// desktopQuote quotes an Exec argument (the Desktop Entry spec).
func desktopQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", "$", `\\$`).Replace(s) + `"`
}
