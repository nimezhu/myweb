package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/nimezhu/myweb/cli"
	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// maxApps is how many apps the Apps submenu lists.
const maxApps = 12

// settings is <home>/desktop.json.
type settings struct {
	Folder string `json:"folder,omitempty"` // the data folder; empty: myweb's default
}

func settingsFile(dirs home.Dirs) string { return filepath.Join(dirs.Root, "desktop.json") }

func loadSettings(dirs home.Dirs) settings {
	var s settings
	if b, err := os.ReadFile(settingsFile(dirs)); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (s settings) save(dirs home.Dirs) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsFile(dirs), append(b, '\n'), 0o600)
}

// exited is a server the tray started that has stopped.
type exited struct {
	cmd  *exec.Cmd
	err  error
	last string // its last line of output
}

// tray is the menu and the server it looks after. Only loop changes it.
type tray struct {
	dirs    home.Dirs
	logFile *os.File
	single  net.Listener

	set        settings
	folder     string       // the data folder served
	child      *exec.Cmd    // the server this tray started, while it runs
	cur        *cli.Running // the server for folder, if one answers
	failed     string       // why the server it started stopped, if it failed
	note       string       // shown instead of the state until the next click
	openWhenUp bool         // open the file manager once the server answers

	openReq chan struct{}
	exits   chan exited
	shown   map[*systray.MenuItem]string // the titles set
	hidden  map[*systray.MenuItem]bool
	iconOn  int // 1 on, 0 off, -1 not set yet

	mStatus, mOpen, mManager, mApps              *systray.MenuItem
	mShow, mChoose, mToggle, mLogin, mLog, mQuit *systray.MenuItem
	appItems                                     []*systray.MenuItem
}

func newTray(dirs home.Dirs, logFile *os.File, single net.Listener, open bool) *tray {
	t := &tray{dirs: dirs, logFile: logFile, single: single, set: loadSettings(dirs), openWhenUp: open,
		openReq: make(chan struct{}, 1), exits: make(chan exited, 4), shown: map[*systray.MenuItem]string{}, hidden: map[*systray.MenuItem]bool{}, iconOn: -1}
	t.folder = t.set.Folder
	if t.folder == "" {
		d, err := cli.DataFolder(program)
		if err != nil {
			log.Printf("data folder: %v", err)
		}
		t.folder = d
	}
	return t
}

func (t *tray) ready() {
	t.setIcon(false)
	systray.SetTooltip("myweb")
	t.mStatus = systray.AddMenuItem("myweb", "")
	t.mStatus.Disable()
	t.mOpen = systray.AddMenuItem("Open file manager", "Open it in the browser, signed in")
	t.mManager = systray.AddMenuItem("Open file manager", "Open it in the browser, signed in")
	t.mManager.Hide()
	t.mApps = systray.AddMenuItem("Apps", "The installed apps, each on a port of its own")
	for i := 0; i < maxApps; i++ {
		it := t.mApps.AddSubMenuItem("app", "")
		it.Hide()
		t.appItems = append(t.appItems, it)
	}
	t.mApps.Hide()
	systray.AddSeparator()
	t.mShow = systray.AddMenuItem("Show data folder", "")
	t.mChoose = systray.AddMenuItem("Choose data folder…", "Serve another folder")
	t.mToggle = systray.AddMenuItem("Stop server", "")
	systray.AddSeparator()
	on := autostartEnabled()
	t.mLogin = systray.AddMenuItemCheckbox("Start at login", "Start myweb when you log in", on)
	if on { // the app may have moved since
		if err := setAutostart(true); err != nil {
			log.Printf("start at login: %v", err)
		}
	}
	t.mLog = systray.AddMenuItem("Show log", "")
	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("Quit myweb", "Stop the server it started, and quit")
	if t.single != nil {
		go serveSingle(t.single, func() {
			select {
			case t.openReq <- struct{}{}:
			default:
			}
		})
	}
	go t.loop()
}

func (t *tray) exit() { release(t.dirs, t.single) }

func (t *tray) loop() {
	t.update()
	if t.cur == nil { // none serves the folder yet
		t.start()
	}
	appClicks := make(chan int)
	for i, it := range t.appItems {
		go func() {
			for range it.ClickedCh {
				appClicks <- i
			}
		}()
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case e := <-t.exits:
			t.reap(e)
		case <-t.openReq:
			t.open(false)
		case <-t.mOpen.ClickedCh:
			t.note = ""
			t.open(false)
		case <-t.mManager.ClickedCh:
			t.note = ""
			t.open(true)
		case i := <-appClicks:
			t.note = ""
			if t.cur != nil && i < len(t.cur.Apps) {
				t.openURL(t.cur.Apps[i].Login)
			}
		case <-t.mShow.ClickedCh:
			t.note = ""
			if err := openPath(t.folder); err != nil {
				t.note = err.Error()
			}
		case <-t.mChoose.ClickedCh:
			t.note = ""
			t.choose()
		case <-t.mToggle.ClickedCh:
			t.note = ""
			t.toggle()
		case <-t.mLogin.ClickedCh:
			t.note = ""
			t.setLogin(!t.mLogin.Checked())
		case <-t.mLog.ClickedCh:
			t.note = ""
			openPath(logPath(t.dirs))
		case <-t.mQuit.ClickedCh:
			t.quit()
			return
		}
		t.update()
	}
}

// ours: the server found is the one this tray started.
func (t *tray) ours() bool {
	return t.cur != nil && t.child != nil && t.cur.PID == t.child.Process.Pid
}

// update finds the server for the folder and shows it.
func (t *tray) update() {
	list, err := cli.Servers(program)
	if err != nil {
		log.Printf("servers: %v", err)
	}
	t.cur = nil
	for i, r := range list {
		if r.Root != t.folder {
			continue
		}
		if t.cur == nil || (t.child != nil && r.PID == t.child.Process.Pid) {
			t.cur = &list[i]
		}
	}
	if t.cur != nil && t.openWhenUp {
		t.openWhenUp = false
		t.openURL(t.cur.Login)
	}

	state := "Stopped"
	switch {
	case t.cur != nil:
		state = "Serving " + tilde(t.folder) + " · port " + strconv.Itoa(t.cur.Port)
	case t.child != nil:
		state = "Starting…"
	case t.failed != "":
		state = "Stopped: " + t.failed
	}
	if t.note != "" {
		state = t.note
	}
	t.title(t.mStatus, shorten(state, 90))
	systray.SetTooltip("myweb · " + shorten(state, 60))
	t.setIcon(t.cur != nil)

	open, app := "Open file manager", ""
	if t.cur != nil && !t.cur.Files { // an app, or a -web folder, at "/"
		first, _, _ := strings.Cut(t.cur.Serves, ", ")
		if name, ok := strings.CutPrefix(first, "app "); ok {
			app = name
		} else {
			app = "site"
		}
		open = "Open " + app
	}
	t.title(t.mOpen, open)
	t.visible(t.mManager, app != "")
	if t.cur == nil {
		t.title(t.mToggle, "Start server")
	} else {
		t.title(t.mToggle, "Stop server")
	}
	var apps []cli.RunningApp
	if t.cur != nil {
		apps = t.cur.Apps
	}
	t.visible(t.mApps, len(apps) > 0)
	for i, it := range t.appItems {
		if i < len(apps) {
			t.title(it, apps[i].Name)
		}
		t.visible(it, i < len(apps))
	}
}

// title and visible change an item only when it differs: each change is a
// message to the desktop, and some menus flicker on each.
func (t *tray) title(it *systray.MenuItem, s string) {
	if t.shown[it] != s {
		t.shown[it] = s
		it.SetTitle(s)
	}
}

func (t *tray) visible(it *systray.MenuItem, on bool) {
	if was, ok := t.hidden[it]; ok && was == !on {
		return
	}
	t.hidden[it] = !on
	if on {
		it.Show()
	} else {
		it.Hide()
	}
}

// start runs a server for the folder: this program with arguments is myweb.
func (t *tray) start() {
	exe, err := os.Executable()
	if err != nil {
		t.failed = err.Error()
		return
	}
	args := []string{"serve", "-no-tui", "-no-open"}
	if t.set.Folder != "" { // else myweb's default, which it creates if missing
		args = append(args, "-d", t.set.Folder)
	}
	cmd := exec.Command(exe, args...)
	out := &tail{w: io.Discard}
	if t.logFile != nil {
		out.w = t.logFile
	}
	cmd.Stdout, cmd.Stderr = out, out
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.failed = err.Error()
		return
	}
	log.Printf("started the server for %s (pid %d)", t.folder, cmd.Process.Pid)
	t.child, t.failed = cmd, ""
	go func() {
		err := cmd.Wait()
		t.exits <- exited{cmd, err, out.last()}
	}()
}

// reap notes that a server it started has stopped.
func (t *tray) reap(e exited) {
	if e.cmd != t.child {
		return
	}
	t.child = nil
	if e.err != nil {
		t.failed = e.last
		if t.failed == "" {
			t.failed = e.err.Error()
		}
		t.openWhenUp = false
		log.Printf("the server stopped: %v", e.err)
	}
}

// open opens what the server serves at "/" (or the file manager), starting
// the server first if none runs.
func (t *tray) open(manager bool) {
	if t.cur == nil {
		t.openWhenUp = true
		if t.child == nil {
			t.start()
		}
		return
	}
	u := t.cur.Login
	if manager {
		u = managerURL(u)
	}
	t.openURL(u)
}

func (t *tray) openURL(u string) {
	if err := console.OpenBrowser(u); err != nil {
		t.note = "could not open the browser: " + err.Error()
	}
}

// managerURL is the file manager's address on the server login opens.
func managerURL(login string) string {
	u, err := url.Parse(login)
	if err != nil {
		return login
	}
	u.Path = myserver.ManagerPath
	return u.String()
}

func (t *tray) toggle() {
	if t.cur == nil {
		t.failed = ""
		t.start()
		return
	}
	if err := t.cur.Stop(); err != nil {
		t.note = "could not stop the server: " + err.Error()
	}
}

func (t *tray) choose() {
	dir, err := pickFolder(t.folder)
	if err != nil {
		t.note = err.Error()
		return
	}
	if dir == "" || dir == t.folder {
		return
	}
	if t.ours() { // a server it didn't start keeps running
		if err := t.cur.Stop(); err != nil {
			t.note = "could not stop the server: " + err.Error()
			return
		}
	}
	t.set.Folder, t.folder, t.failed = dir, dir, ""
	if err := t.set.save(t.dirs); err != nil {
		log.Printf("settings: %v", err)
	}
	t.openWhenUp = true
	t.update()
	if t.cur == nil {
		t.start()
	}
}

func (t *tray) setLogin(on bool) {
	if err := setAutostart(on); err != nil {
		t.note = "start at login: " + err.Error()
		return
	}
	if on {
		t.mLogin.Check()
	} else {
		t.mLogin.Uncheck()
	}
}

// quit stops the server it started (one it found running keeps running).
func (t *tray) quit() {
	if t.child != nil {
		if t.ours() {
			if err := t.cur.Stop(); err != nil {
				log.Printf("stopping the server: %v", err)
				t.child.Process.Kill()
			}
		} else {
			t.child.Process.Kill()
		}
	}
	systray.Quit()
}

func (t *tray) setIcon(on bool) {
	v := 0
	if on {
		v = 1
	}
	if t.iconOn == v {
		return
	}
	t.iconOn = v
	setTrayIcon(on)
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// tail passes output on and keeps its last line.
type tail struct {
	w    io.Writer
	mu   sync.Mutex
	line []byte
	prev []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	for _, l := range bytes.SplitAfter(p, []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		t.line = append(t.line, l...)
		if l[len(l)-1] == '\n' {
			if s := bytes.TrimSpace(t.line); len(s) > 0 {
				t.prev = append(t.prev[:0], s...)
			}
			t.line = t.line[:0]
		}
	}
	t.mu.Unlock()
	return t.w.Write(p)
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := bytes.TrimSpace(t.line); len(s) > 0 {
		return string(s)
	}
	return string(t.prev)
}
