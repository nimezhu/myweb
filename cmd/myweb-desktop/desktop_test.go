package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
	go serveSingle(l, func(msg string) { opened <- msg == "open" })
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
