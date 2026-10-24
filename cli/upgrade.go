package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/selfupdate"
)

// "upgrade" replaces the binary with a release from GitHub:
//
//	upgrade              the latest release, if it is newer
//	upgrade VERSION      that release, newer or older
//	upgrade -check       only tell whether there is a newer one
//	upgrade -rollback    go back to the binary the last upgrade replaced
//	upgrade -no-restart  leave the installed services running the old binary
//
// Afterwards the services running this binary are restarted; other running
// servers are listed, to restart by hand.

// executable is the binary to replace; replaced in tests.
var executable = defaultExecutable

func defaultExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// newUpdater is replaced in tests.
var newUpdater = defaultUpdater

func defaultUpdater(program string) *selfupdate.Updater {
	return &selfupdate.Updater{Program: program, Current: myserver.VERSION}
}

func upgradeCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "upgrade")
	check := fl.Bool("check", false, "")
	rollback := fl.Bool("rollback", false, "")
	noRestart := fl.Bool("no-restart", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (len(pos) == 1 && (*check || *rollback)) || (*check && *rollback) {
		return usageError{errors.New("upgrade takes a VERSION, -check or -rollback")}
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	if strings.Contains(exe, "go-build") {
		return errors.New("this is a temporary build (go run); nothing to upgrade")
	}
	if *rollback {
		if err := selfupdate.Rollback(exe); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is back to the previous version (run %s version)\n", exe, spec.Name)
		if !*noRestart {
			restartServices(w, spec, *fl.home, exe, "")
		}
		return nil
	}

	u := newUpdater(spec.Name)
	ctx := context.Background()
	var rel *selfupdate.Release
	if len(pos) == 1 {
		rel, err = u.Get(ctx, pos[0])
	} else {
		rel, err = u.Latest(ctx)
	}
	if err != nil {
		return err
	}
	if len(pos) == 0 && !selfupdate.Newer(rel.Version, u.Current) {
		fmt.Fprintf(w, "%s %s is the latest version\n", spec.Name, u.Current)
		return nil
	}
	if *check {
		fmt.Fprintf(w, "%s %s is available (this is %s): run %s upgrade\n  %s\n", spec.Name, rel.Version, u.Current, spec.Name, rel.URL)
		return nil
	}
	if rel.Version == u.Current {
		fmt.Fprintf(w, "%s is already %s\n", spec.Name, u.Current)
		return nil
	}
	fmt.Fprintf(w, "downloading %s %s…\n", spec.Name, rel.Version)
	if err := u.Install(ctx, rel, exe); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s → %s (%s; %s upgrade -rollback goes back)\n", spec.Name, u.Current, rel.Version, exe, spec.Name)
	if notes := strings.TrimSpace(rel.Notes); notes != "" && len(pos) == 0 {
		fmt.Fprintf(w, "\n%s\n", notes)
	}
	if *noRestart {
		restartServices(w, spec, *fl.home, "", rel.Version)
	} else {
		restartServices(w, spec, *fl.home, exe, rel.Version)
	}
	return nil
}

// restartServices restarts the services that run exe (none when exe is ""),
// then lists the servers still running another version than version (when
// given), to restart by hand.
func restartServices(w io.Writer, spec Spec, homeDir, exe, version string) {
	dirs, err := homeFor(spec.Name, homeDir)
	if err != nil {
		return
	}
	before := runningServers(dirs)
	restarted := 0
	if exe != "" {
		for _, s := range services(runtime.GOOS, exe) {
			ok, err := s.restart(before)
			switch {
			case err != nil:
				fmt.Fprintf(w, "could not restart the service %s: %v\n", s.name, err)
			case ok:
				fmt.Fprintf(w, "restarted the service %s\n", s.name)
				restarted++
			}
		}
	}
	if version == "" {
		return
	}
	old := func(list []server) []server {
		var out []server
		for _, s := range list {
			if s.Version != version {
				out = append(out, s)
			}
		}
		return out
	}
	// the restarted ones come back with the new version
	want := max(0, len(old(before))-restarted)
	stale := old(runningServers(dirs))
	for deadline := time.Now().Add(10 * time.Second); len(stale) > want && time.Now().Before(deadline); stale = old(runningServers(dirs)) {
		time.Sleep(200 * time.Millisecond)
	}
	if len(stale) > 0 {
		fmt.Fprintf(w, "\nstill running the old version until restarted:\n")
		for _, s := range stale {
			fmt.Fprintf(w, "  pid %d  %s  %s\n", s.PID, s.address(), tildePath(s.Root))
		}
		fmt.Fprintf(w, "(%s stop -all stops them)\n", spec.Name)
	}
}

// services is replaced in tests.
var services = installedServices

// noticeUpgrade logs, once the server runs, that a newer release exists.
// GitHub is asked at most once a day.
func noticeUpgrade(name string, dirs home.Dirs, logf func(string, ...interface{})) {
	go func() {
		time.Sleep(2 * time.Second) // after the console has started
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		u := newUpdater(name)
		if v := u.Cached(ctx, filepath.Join(dirs.Root, "upgrade-check.json"), 24*time.Hour); v != "" && selfupdate.Newer(v, u.Current) {
			logf("%s %s is available (this is %s): run %s upgrade", name, v, u.Current, name)
		}
	}()
}
