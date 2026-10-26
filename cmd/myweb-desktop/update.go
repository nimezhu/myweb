package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/selfupdate"
)

// Updates come from the same signed GitHub releases as "myweb upgrade":
// myweb-desktop_VERSION_OS_ARCH on Linux and Windows, and on macOS
// myweb-desktop_VERSION_darwin_universal.zip, the whole myweb.app.

var newUpdater = func() *selfupdate.Updater {
	return &selfupdate.Updater{Program: "myweb-desktop", Current: myserver.VERSION}
}

// checkFile keeps the latest version, asked of GitHub at most once a day
// (myweb's own check uses it too: they are released together).
func checkFile(dirs home.Dirs) string { return filepath.Join(dirs.Root, "upgrade-check.json") }

// installed is what an update replaces: the binary, and on macOS the app
// around it ("" when it doesn't run from one).
func installed() (exe, app string, err error) {
	if exe, err = os.Executable(); err != nil {
		return "", "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", "", err
	}
	if strings.Contains(exe, "go-build") {
		return "", "", errors.New("this is a temporary build (go run): nothing to update")
	}
	return exe, appBundle(exe), nil
}

// appBundle is the .app that exe is the program of, if it is one.
func appBundle(exe string) string {
	macOS := filepath.Dir(exe)
	contents := filepath.Dir(macOS)
	app := filepath.Dir(contents)
	if filepath.Base(macOS) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(app, ".app") {
		return app
	}
	return ""
}

// install puts rel in place of the program at exe (or the app around it).
func install(ctx context.Context, u *selfupdate.Updater, rel *selfupdate.Release, exe, app, goos string) error {
	if goos == "darwin" {
		if app == "" {
			return errors.New("on macOS only myweb.app updates itself: download it from " + rel.URL)
		}
		asset := fmt.Sprintf("%s_%s_darwin_universal.zip", u.Program, rel.Version)
		return u.InstallApp(ctx, rel, asset, app, filepath.Join("Contents", "MacOS", filepath.Base(exe)))
	}
	return u.Install(ctx, rel, exe)
}

// relaunch starts the program at exe again, without opening the browser.
func relaunch(exe string) error {
	cmd := exec.Command(exe, autostartArg)
	hideWindow(cmd)
	newSession(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// upgradeCmd is "myweb-desktop upgrade [-check] [VERSION]"; a tray that
// runs is then asked to restart into it.
func upgradeCmd(dirs home.Dirs, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("myweb-desktop upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "only tell whether there is a newer version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && *check) {
		return errors.New("usage: myweb-desktop upgrade [-check | VERSION]")
	}
	u := newUpdater()
	ctx := context.Background()
	var rel *selfupdate.Release
	var err error
	if fs.NArg() == 1 {
		rel, err = u.Get(ctx, fs.Arg(0))
	} else {
		rel, err = u.Latest(ctx)
	}
	if err != nil {
		return err
	}
	if fs.NArg() == 0 && !selfupdate.Newer(rel.Version, u.Current) {
		fmt.Fprintf(w, "myweb-desktop %s is the latest version\n", u.Current)
		return nil
	}
	if *check {
		fmt.Fprintf(w, "myweb-desktop %s is available (this is %s): run myweb-desktop upgrade\n  %s\n", rel.Version, u.Current, rel.URL)
		return nil
	}
	if rel.Version == u.Current {
		fmt.Fprintf(w, "myweb-desktop is already %s\n", u.Current)
		return nil
	}
	exe, app, err := installed()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "downloading myweb-desktop %s…\n", rel.Version)
	if err := install(ctx, u, rel, exe, app, runtime.GOOS); err != nil {
		return err
	}
	where := exe
	if app != "" {
		where = app
	}
	fmt.Fprintf(w, "myweb-desktop %s → %s (%s)\n", u.Current, rel.Version, where)
	if b, err := os.ReadFile(addrFile(dirs)); err == nil && ask(strings.TrimSpace(string(b)), "restart") {
		fmt.Fprintln(w, "the tray app is restarting into it")
	}
	return nil
}

// latest is the newest version, asked of GitHub at most once a day ("" when
// it can't be reached).
func latest(dirs home.Dirs, fresh bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := newUpdater()
	if !fresh {
		return u.Cached(ctx, checkFile(dirs), 24*time.Hour), nil
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		return "", err
	}
	return rel.Version, nil
}
