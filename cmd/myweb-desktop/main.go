// myweb-desktop is myweb in the system tray (the menu bar on macOS): it serves
// your data folder while it runs, and its menu opens the file manager and the
// apps, picks the folder, stops and starts the server, and starts the app at
// login. Starting it again opens the file manager.
//
// With arguments it is the myweb command line (myweb-desktop status, …); it
// starts its server that way, as "myweb-desktop serve -no-tui -no-open -d DIR".
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/systray"

	"github.com/nimezhu/myweb/cli"
	"github.com/nimezhu/myweb/home"
)

// program names the home (~/.myweb) and the servers: the same as myweb's.
const program = "myweb"

// maxLog is the size past which the log starts afresh (at the next start).
const maxLog = 5 << 20

func main() {
	args := os.Args[1:]
	if len(args) > 0 && strings.HasPrefix(args[0], "-psn_") { // old macOS adds this to an app's arguments
		args = args[1:]
	}
	login := len(args) == 1 && args[0] == autostartArg
	if len(args) > 0 && !login {
		os.Exit(cli.Main(cli.Spec{Name: program, Args: args}))
	}
	dirs, err := home.For(program)
	if err != nil {
		fmt.Fprintln(os.Stderr, "myweb-desktop:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Join(dirs.Root, "logs"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "myweb-desktop:", err)
		os.Exit(1)
	}
	single, handed := claim(dirs, !login)
	if handed { // already running: that one opens the file manager
		return
	}
	// started from a desktop, nothing reads stderr: the log is where to look;
	// it holds the server's requests too, so the previous one is kept once big
	if fi, err := os.Stat(logPath(dirs)); err == nil && fi.Size() > maxLog {
		os.Rename(logPath(dirs), logPath(dirs)+".1")
	}
	logFile, err := os.OpenFile(logPath(dirs), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	t := newTray(dirs, logFile, single, !login)
	systray.Run(t.ready, t.exit)
}

func logPath(dirs home.Dirs) string { return filepath.Join(dirs.Root, "logs", "desktop.log") }

// tilde shortens a path in the home folder to ~/….
func tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && (p == h || strings.HasPrefix(p, h+string(filepath.Separator))) {
		return "~" + p[len(h):]
	}
	return p
}
