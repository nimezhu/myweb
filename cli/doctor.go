package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/selfupdate"
	"github.com/nimezhu/myweb/webapp"
)

// "doctor" checks what myweb needs and says what to do about what's wrong:
//
//	doctor [-offline] [-home DIR]
//
// It changes nothing. Its exit status is 1 when something is broken.

// doctor collects the results.
type doctor struct {
	w        io.Writer
	problems int
}

func (d *doctor) section(name string) { fmt.Fprintf(d.w, "\n%s\n", name) }

func (d *doctor) ok(format string, a ...interface{}) {
	fmt.Fprintf(d.w, "  ✓ %s\n", fmt.Sprintf(format, a...))
}

// warn: works, but there is something to know or do.
func (d *doctor) warn(hint, format string, a ...interface{}) {
	fmt.Fprintf(d.w, "  ! %s\n", fmt.Sprintf(format, a...))
	if hint != "" {
		fmt.Fprintf(d.w, "      → %s\n", hint)
	}
}

// bad: something doesn't work.
func (d *doctor) bad(hint, format string, a ...interface{}) {
	d.problems++
	fmt.Fprintf(d.w, "  ✗ %s\n", fmt.Sprintf(format, a...))
	if hint != "" {
		fmt.Fprintf(d.w, "      → %s\n", hint)
	}
}

func doctorCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "doctor")
	offline := fl.Bool("offline", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{fmt.Errorf("doctor takes no arguments")}
	}
	name := spec.Name
	d := &doctor{w: w}
	ctx := context.Background()

	// the binary
	d.section(name + " " + myserver.VERSION)
	exe, exeErr := executable()
	switch {
	case exeErr != nil:
		d.warn("", "can't tell where this binary is: %v", exeErr)
	case strings.Contains(exe, "go-build"):
		d.warn("", "a temporary build (go run)")
	default:
		d.ok("binary %s", tildePath(exe))
		if found, err := exec.LookPath(name); err != nil {
			d.warn("add "+tildePath(filepath.Dir(exe))+" to PATH", "%s is not on PATH", name)
		} else if real, _ := filepath.EvalSymlinks(found); real != exe {
			d.warn("remove it, or put "+tildePath(filepath.Dir(exe))+" first on PATH", "%s on PATH is another binary: %s", name, tildePath(found))
		}
		if err := canWrite(filepath.Dir(exe)); err != nil {
			d.warn(name+" upgrade needs to run as a user who may write there (e.g. sudo)", "can't write %s, where upgrades go", tildePath(filepath.Dir(exe)))
		}
	}
	if !*offline {
		u := newUpdater(name)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rel, err := u.Latest(cctx)
		cancel()
		switch {
		case err != nil:
			d.warn("check the network, or run doctor -offline", "can't ask GitHub for releases: %v", err)
		case selfupdate.Newer(rel.Version, u.Current):
			d.warn(name+" upgrade", "%s %s is out (this is %s)", name, rel.Version, u.Current)
		default:
			d.ok("the latest release")
		}
	}

	// home, config, data folder, port
	dirs, err := homeFor(name, *fl.home)
	if err != nil {
		d.bad("", "home: %v", err)
		return d.result()
	}
	d.section("home " + tildePath(dirs.Root))
	if fi, err := os.Stat(dirs.Root); err != nil {
		d.warn("", "not made yet: the first %s makes it", name)
	} else if !fi.IsDir() {
		d.bad("move it away", "%s is not a folder", tildePath(dirs.Root))
	} else if err := canWrite(dirs.Root); err != nil {
		d.bad("fix its permissions (it must be yours)", "can't write it: %v", err)
	} else {
		d.ok("writable")
	}
	fs, sf := newServeFlags(spec)
	fs.SetOutput(io.Discard)
	cfg, err := loadConfig(dirs)
	if err != nil {
		d.bad("fix or remove the file ("+name+" config shows it)", "config: %v", err)
	} else if err := cfg.apply(fs, configFile(dirs)); err != nil {
		d.bad(name+" config unset NAME removes a bad setting", "config: %v", err)
	} else if len(cfg) > 0 {
		d.ok("config: %s", strings.Join(cfg.keys(), ", "))
	}
	data := dirs.Data()
	if *sf.directory != "" {
		data, _ = filepath.Abs(expandHome(*sf.directory))
	}
	if fi, err := os.Stat(data); err != nil {
		if *sf.directory == "" {
			d.ok("data folder %s: made when first served", tildePath(data))
		} else {
			d.bad("make it, or "+name+" config set d FOLDER", "data folder %s: %v", tildePath(data), errors.Unwrap(err))
		}
	} else if !fi.IsDir() {
		d.bad("", "data folder %s is not a folder", tildePath(data))
	} else if err := canWrite(data); err != nil {
		d.warn("serve it with -read-only, or fix its permissions", "data folder %s is read-only for you", tildePath(data))
	} else {
		d.ok("data folder %s", tildePath(data))
	}

	running := runningServers(dirs)
	port := *sf.port
	if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		ln.Close()
		d.ok("port %d is free", port)
	} else {
		mine := false
		for _, s := range running {
			mine = mine || s.Port == port
		}
		if mine {
			d.ok("port %d: %s serves it", port, name)
		} else if v := otherMyweb(port); v != "" {
			d.warn("it was started from another -home, or by an older "+name+": stop it, or let "+name+" take the next free port",
				"port %d is taken by a %s %s that this home doesn't know", port, name, v)
		} else {
			d.warn("another program uses it; "+name+" takes the next free port, or set one: "+name+" config set p PORT", "port %d is taken", port)
		}
	}

	// running servers
	d.section("running")
	if len(running) == 0 {
		d.ok("no server running")
	}
	for _, s := range running {
		if s.Version != "" && s.Version != myserver.VERSION {
			d.warn(name+" stop "+strconv.Itoa(s.PID)+", then start it again (a service: "+name+" upgrade restarts it)",
				"pid %d %s runs %s %s, not this %s", s.PID, s.address(), name, s.Version, myserver.VERSION)
		} else {
			d.ok("pid %d  %s  %s", s.PID, s.address(), tildePath(s.Root))
		}
	}
	if exeErr == nil && !strings.Contains(exe, "go-build") {
		for _, svc := range installedServices(runtime.GOOS, exe) {
			switch runtime.GOOS {
			case "linux":
				if out, _ := runCmd("systemctl", "--user", "is-active", svc.name+".service"); out == "active" {
					d.ok("service %s is running", svc.name)
				} else {
					d.warn("journalctl --user -u "+svc.name+" says why; "+name+" service status", "service %s is installed but %s", svc.name, orUnknown(out))
				}
			default:
				d.ok("service %s is installed (%s service status -as %s)", svc.name, name, svc.name)
			}
		}
	}

	// apps and catalogs
	d.section("apps")
	apps, err := webapp.InstalledApps(dirs.Apps())
	if err != nil {
		d.bad("", "can't read %s: %v", tildePath(dirs.Apps()), err)
	} else if len(apps) == 0 {
		d.ok("none installed")
	}
	for _, a := range apps {
		switch {
		case a.Current == "":
			d.bad(name+" app update "+a.Name+" (or remove it)", "%s: no version installed", a.Name)
		case a.Key == "":
			d.warn(name+" app remove "+a.Name+", then install it again with its key", "%s %s: installed before keys were kept, so its updates can't be checked", a.Name, a.Current)
		case a.Pin != "":
			d.ok("%s %s (kept at this version: %s app use %s latest follows updates)", a.Name, a.Current, name, a.Name)
		default:
			d.ok("%s %s", a.Name, a.Current)
		}
		for _, f := range a.Access {
			if f.Path == "/" {
				d.warn(name+" app deny "+a.Name+" / takes it back", "%s may use the whole data folder", a.Name)
			}
		}
	}
	cats := catalogsFor(dirs)
	srcs, err := cats.Sources()
	switch {
	case err != nil:
		d.bad("fix or remove "+tildePath(cats.File), "catalogs: %v", err)
	case len(srcs) == 0:
		d.ok("no catalogs (%s app catalog add URL -key KEY adds one)", name)
	}
	for _, src := range srcs {
		if *offline {
			d.ok("catalog %s", src.URL)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cat, _, err := cats.Fetch(cctx, src, true)
		cancel()
		if err != nil {
			d.bad("check the address and key, or "+name+" app catalog remove "+src.URL, "catalog %s: %v", src.URL, err)
		} else {
			d.ok("catalog %s: %d app%s, signature checked", src.URL, len(cat.Apps), plural(len(cat.Apps)))
		}
	}
	return d.result()
}

func (d *doctor) result() error {
	if d.problems > 0 {
		fmt.Fprintf(d.w, "\n%d problem%s\n", d.problems, plural(d.problems))
		return silentExit(1)
	}
	fmt.Fprintln(d.w, "\nno problems")
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func orUnknown(s string) string {
	if s == "" {
		return "not running"
	}
	return s
}

// otherMyweb is the version of a myweb answering on port, if one does.
func otherMyweb(port int) string {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/version")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	v := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK || !versionLike.MatchString(v) {
		return ""
	}
	return v
}

var versionLike = regexp.MustCompile(`^\d+\.\d+\.\d+[0-9A-Za-z.+-]*$`)

// canWrite tries to make a file in dir.
func canWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}
