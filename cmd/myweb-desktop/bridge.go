package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/nimezhu/myweb/cli"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// Home's Settings and first run reach the tray through the server it started
// (cli/desktopcmd.go): each request comes in on the tray's local address and
// is answered by loop, which owns the tray's state.

// homeAsk is a request from Home, and where its answer goes.
type homeAsk struct {
	req   cli.DesktopRequest
	reply chan cli.DesktopReply
}

// answer passes a request on to loop and returns its answer, as JSON.
func (t *tray) answer(line []byte) []byte {
	var out cli.DesktopReply
	var req cli.DesktopRequest
	if err := json.Unmarshal(line, &req); err != nil {
		out.Error = "a request myweb doesn't understand"
	} else {
		a := homeAsk{req, make(chan cli.DesktopReply, 1)}
		select {
		case t.asks <- a:
			out = <-a.reply
		case <-time.After(10 * time.Second): // busy, e.g. with a dialog from the menu
			out.Error = "myweb is busy: try again in a moment"
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// handle does what Home asks, in loop. A change of folder or a restart stops
// the server that passes the answer on, so they happen a moment later.
func (t *tray) handle(r cli.DesktopRequest) (out cli.DesktopReply) {
	fail := func(err error) {
		if err != nil && out.Error == "" {
			out.Error = err.Error()
		}
	}
	switch {
	case r.Pick:
		dir, err := pickFolder(t.folder)
		fail(err)
		out.Picked, out.PickedShort = dir, tilde(dir)
	case r.Show == "folder":
		fail(openPath(t.folder))
	case r.Show == "log":
		fail(openPath(logPath(t.dirs)))
	case r.Check:
		t.note = ""
		if t.newer == "" {
			t.checkOrUpdate() // the answer comes back to loop; Home asks again
		} else {
			t.download()
		}
	case r.Restart:
		if t.staged == "" || t.updating {
			fail(errors.New("no update is ready"))
			break
		}
		time.AfterFunc(700*time.Millisecond, func() {
			select {
			case t.restartReq <- struct{}{}:
			default:
			}
		})
	}
	if r.Login != nil && *r.Login != autostartEnabled() {
		fail(t.setLogin(*r.Login))
	}
	if r.AutoUpdate != nil {
		if t.set.Updates = ""; !*r.AutoUpdate {
			t.set.Updates = "ask"
		}
		fail(t.set.save(t.dirs))
		t.download()
	}
	if r.Welcomed && t.set.FirstRun {
		t.set.FirstRun = false
		fail(t.set.save(t.dirs))
	}
	if r.Folder != "" && r.Folder != t.folder {
		if fi, err := os.Stat(r.Folder); err != nil || !fi.IsDir() || !filepath.IsAbs(r.Folder) {
			fail(errors.New(tilde(r.Folder) + " isn't a folder"))
		} else if out.Error == "" {
			out.Moving = true
			dir := r.Folder
			time.AfterFunc(700*time.Millisecond, func() { t.moveReq <- dir })
		}
	}
	out.DesktopState = t.state()
	return out
}

// state is the tray's settings and state, for Home.
func (t *tray) state() cli.DesktopState {
	return cli.DesktopState{
		Folder: t.folder, FolderShort: tilde(t.folder), Login: autostartEnabled(),
		AutoUpdate: t.set.Updates != "ask", FirstRun: t.set.FirstRun, Version: myserver.VERSION,
		Newer: t.newer, Ready: t.staged, Updating: t.updating, CantUpdate: t.cantUpdate, Note: t.note, OS: runtime.GOOS,
	}
}

// newInstallFolder is where a new install keeps its files: Documents/myweb
// (else ~/myweb), when nothing says otherwise yet. ok is false for a home
// that has been used: it keeps its folder (def, myweb's default).
func newInstallFolder(dirs home.Dirs, def string) (string, bool) {
	if _, err := os.Stat(settingsFile(dirs)); err == nil {
		return "", false
	}
	if def != dirs.Data() { // "myweb config set d DIR" chose one
		return "", false
	}
	if _, err := os.Stat(def); err == nil {
		return "", false
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	dir := filepath.Join(h, "myweb")
	if fi, err := os.Stat(filepath.Join(h, "Documents")); err == nil && fi.IsDir() {
		dir = filepath.Join(h, "Documents", "myweb")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("files folder: %v", err)
		return "", false
	}
	return dir, true
}
