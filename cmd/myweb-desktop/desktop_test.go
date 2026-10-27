package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/cli"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/selfupdate"
)

func TestTailKeepsLastLine(t *testing.T) {
	var out strings.Builder
	tl := &tail{w: &out}
	fmt.Fprint(tl, "first\nsec")
	fmt.Fprint(tl, "ond\n\n")
	if got := tl.last(); got != "second" {
		t.Errorf("last = %q, want second", got)
	}
	fmt.Fprint(tl, "myweb: root directory \"x\" does not exist")
	if got := tl.last(); !strings.HasPrefix(got, "myweb: root") {
		t.Errorf("an unfinished line: last = %q", got)
	}
	if out.String() != "first\nsecond\n\nmyweb: root directory \"x\" does not exist" {
		t.Errorf("passed on %q", out.String())
	}
}

func TestManagerURL(t *testing.T) {
	got := managerURL("http://127.0.0.1:8433/?token=abc")
	if got != "http://127.0.0.1:8433/_myweb/?token=abc" {
		t.Errorf("managerURL = %s", got)
	}
	if got := pageURL("http://127.0.0.1:8433/?token=abc", homePath); got != "http://127.0.0.1:8433/_myweb/home.html?token=abc" {
		t.Errorf("Home = %s", got)
	}
	if got := pageURL("http://127.0.0.1:8433/?token=abc", ""); got != "http://127.0.0.1:8433/?token=abc" {
		t.Errorf(`"" = %s`, got)
	}
}

func TestAutostartEntry(t *testing.T) {
	linux := autostartEntry("linux", `/home/a b/"q"/$x/my\web`)
	if !strings.Contains(linux, "\nExec=\"/home/a b/\\\\\"q\\\\\"/\\\\$x/my\\\\\\\\web\" -autostart\n") {
		t.Errorf("linux entry:\n%s", linux)
	}
	mac := autostartEntry("darwin", "/Applications/A&B.app/Contents/MacOS/myweb-desktop")
	if !strings.Contains(mac, "<string>/Applications/A&amp;B.app/Contents/MacOS/myweb-desktop</string>") ||
		!strings.Contains(mac, "<string>-autostart</string>") {
		t.Errorf("mac entry:\n%s", mac)
	}
}

func TestOneTrayPerHome(t *testing.T) {
	dirs := home.Dirs{Root: t.TempDir()}
	l, handed := claim(dirs, true)
	if handed || l == nil {
		t.Fatal("the first start should run")
	}
	opened := make(chan bool, 2)
	go serveSingle(l, nil, func(msg string) { opened <- msg == "open" })
	if _, handed := claim(dirs, true); !handed {
		t.Fatal("a second start should hand over")
	}
	if !<-opened {
		t.Error("the first should be asked to open")
	}
	if _, handed := claim(dirs, false); !handed { // at login: hand over quietly
		t.Fatal("a second start at login should hand over")
	}
	select {
	case <-opened:
		t.Error("a start at login should not open")
	default:
	}
	release(dirs, l)
	l2, handed := claim(dirs, true)
	if handed || l2 == nil {
		t.Fatal("after the first quits, a start should run")
	}
	release(dirs, l2)
}

func TestAppBundle(t *testing.T) {
	for exe, want := range map[string]string{
		"/Applications/myweb.app/Contents/MacOS/myweb-desktop": "/Applications/myweb.app",
		"/usr/local/bin/myweb-desktop":                         "",
		"/x/Contents/MacOS/myweb-desktop":                      "",
	} {
		if got := appBundle(exe); got != want {
			t.Errorf("appBundle(%s) = %q, want %q", exe, got, want)
		}
	}
}

func TestMacUpdatesOnlyTheApp(t *testing.T) {
	err := install(context.Background(), newUpdater(), &selfupdate.Release{Version: "9.9.9", URL: "page"}, "/usr/local/bin/myweb-desktop", "", "darwin")
	if err == nil || !strings.Contains(err.Error(), "only myweb.app") {
		t.Fatalf("err = %v", err)
	}
}

// A new install keeps its files in Documents/myweb; a home in use keeps its folder.
func TestNewInstallFolder(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	dirs := home.Dirs{Root: filepath.Join(h, ".myweb")}
	os.MkdirAll(dirs.Root, 0o700)
	if got, ok := newInstallFolder(dirs, dirs.Data()); !ok || got != filepath.Join(h, "myweb") {
		t.Errorf("no Documents: %q %v", got, ok)
	}
	os.Mkdir(filepath.Join(h, "Documents"), 0o755)
	if got, ok := newInstallFolder(dirs, dirs.Data()); !ok || got != filepath.Join(h, "Documents", "myweb") {
		t.Errorf("with Documents: %q %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(h, "Documents", "myweb")); err != nil {
		t.Error("the folder wasn't made")
	}
	if _, ok := newInstallFolder(dirs, "/somewhere/chosen"); ok {
		t.Error("a folder set with myweb config was replaced")
	}
	os.Mkdir(dirs.Data(), 0o755)
	if _, ok := newInstallFolder(dirs, dirs.Data()); ok {
		t.Error("a home with files was treated as new")
	}
	os.Remove(dirs.Data())
	(settings{}).save(dirs)
	if _, ok := newInstallFolder(dirs, dirs.Data()); ok {
		t.Error("a home with desktop settings was treated as new")
	}
}

// Home's requests come in on the tray's address as "json {…}" lines.
func TestHomeAsks(t *testing.T) {
	h := t.TempDir()
	dirs := home.Dirs{Root: h}
	folder, other := filepath.Join(h, "files"), filepath.Join(h, "other")
	os.Mkdir(folder, 0o755)
	os.Mkdir(other, 0o755)
	tr := &tray{dirs: dirs, folder: folder, set: settings{Folder: folder, FirstRun: true},
		asks: make(chan homeAsk), moveReq: make(chan string, 1), restartReq: make(chan struct{}, 1)}
	go func() {
		for a := range tr.asks {
			a.reply <- tr.handle(a.req)
		}
	}()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveSingle(l, tr.answer, func(string) {})
	do := func(req string) cli.DesktopReply {
		t.Helper()
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "json %s\n", req)
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var out cli.DesktopReply
		if err := json.Unmarshal(line, &out); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
	if r := do(`{}`); r.Error != "" || !r.FirstRun || !r.AutoUpdate || r.Folder != folder {
		t.Fatalf("state: %+v", r)
	}
	if r := do(`{"autoUpdate":false,"welcomed":true}`); r.Error != "" || r.AutoUpdate || r.FirstRun {
		t.Fatalf("settings: %+v", r)
	}
	if s := loadSettings(dirs); s.Updates != "ask" || s.FirstRun {
		t.Fatalf("saved: %+v", s)
	}
	if r := do(`{"restart":true}`); r.Error == "" {
		t.Fatal("a restart with no update ready")
	}
	if r := do(`{"folder":"` + filepath.Join(h, "missing") + `"}`); r.Error == "" || r.Moving {
		t.Fatalf("a missing folder: %+v", r)
	}
	if r := do(`{"folder":"` + other + `"}`); r.Error != "" || !r.Moving {
		t.Fatalf("move: %+v", r)
	}
	select {
	case dir := <-tr.moveReq:
		if dir != other {
			t.Fatalf("moved to %s", dir)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the move wasn't asked for")
	}
	if r := do(`not json`); r.Error == "" {
		t.Fatal("bad JSON was accepted")
	}
}
