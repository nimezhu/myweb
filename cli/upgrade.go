package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if dirs, err := homeFor(spec.Name, *fl.home); err == nil {
		if old := runningServers(dirs); len(old) > 0 {
			fmt.Fprintf(w, "\nstill running the old version until restarted:\n")
			for _, s := range old {
				fmt.Fprintf(w, "  pid %d  %s  %s\n", s.PID, s.address(), tildePath(s.Root))
			}
			fmt.Fprintf(w, "(%s stop -all stops them; a service restarts with %s service install, with its flags)\n", spec.Name, spec.Name)
		}
	}
	return nil
}

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
