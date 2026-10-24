package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// registerAppCmd serves the app's status at POST /cmd/app; {"check":true}
// also starts a check for an update (admins only, when multi-user).
func registerAppCmd(srv *myserver.Server, app *webapp.Installer) {
	srv.RegisterCmd("app", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
		var p struct {
			Check     bool   `json:"check"`     // check for an update now
			Refresh   bool   `json:"refresh"`   // pick up a change "myweb app use" saved
			Use       string `json:"use"`       // a version, or "latest"
			Available bool   `json:"available"` // include every published version
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if (p.Check || p.Refresh || p.Use != "") && req.User != nil && req.User.Role != "admin" {
			return nil, myserver.Errorf(http.StatusForbidden, "only admins can change the app")
		}
		if p.Refresh {
			app.Refresh()
		}
		if p.Check {
			app.CheckNow()
		}
		if p.Use != "" {
			if err := app.Use(ctx, p.Use); err != nil {
				return nil, myserver.Errorf(http.StatusBadRequest, "%v", err)
			}
		}
		res := struct {
			webapp.Status
			Available []webapp.Manifest `json:"available,omitempty"`
		}{Status: app.Status()}
		if p.Available {
			vs, err := app.Available(ctx)
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadGateway, "%v", err)
			}
			res.Available = vs.Versions
		}
		return res, nil
	}, myserver.Describe(`the web app at /: versions, release notes, update status. {"check":true} checks for an update now; {"use":"1.2.0"} serves that version and stops updates ("latest" follows them again); {"available":true} adds every published version`))
}

// appCmd runs "app …": managing the web apps installed with -web-from.
func appCmd(spec Spec, args []string) int {
	sub := "list"
	if len(args) > 0 && isHelpArg(args[0]) {
		return finish(spec, "app", flag.ErrHelp)
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "run" {
		return appRun(spec, args)
	}
	cmds := map[string]func(name string, args []string) error{
		"list": appList, "ls": appList,
		"install":  appInstall,
		"update":   appUpdate,
		"versions": appVersions,
		"remove":   appRemove, "rm": appRemove,
		"default": appDefault,
		"use":     appUse,
		"catalog": appCatalog,
		"allow":   appAllow,
		"deny":    appDeny,
		"access":  appAccess,
	}
	f, ok := cmds[sub]
	if !ok {
		return finish(spec, "app", usageError{fmt.Errorf("unknown command \"app %s\"", sub)})
	}
	return finish(spec, "app", f(spec.Name, args))
}

// appFlags is a flag set with -home; parse accepts flags before and after
// the names.
type appFlags struct {
	*flag.FlagSet
	name string
	home *string
}

// newAppFlags is for the command cmd, e.g. "app use"; errors and -h are
// reported by finish, with the command's help topic.
func newAppFlags(name, cmd string) appFlags {
	fs := flag.NewFlagSet(name+" "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return appFlags{fs, name, fs.String("home", "", fmt.Sprintf("%s's own folder (default $%s, or ~/.%s)", name, home.EnvVar(name), name))}
}

func (f appFlags) parse(args []string) ([]string, error) {
	var names []string
	for {
		if err := f.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{err}
		}
		if args = f.Args(); len(args) == 0 {
			return names, nil
		}
		names, args = append(names, args[0]), args[1:]
	}
}

// oneApp parses the flags and finds the one app they name.
func (f appFlags) oneApp(args []string) (webapp.App, error) {
	names, err := f.parse(args)
	if err != nil {
		return webapp.App{}, err
	}
	if len(names) != 1 {
		return webapp.App{}, fmt.Errorf("usage: %s NAME", f.Name())
	}
	dirs, err := homeFor(f.name, *f.home)
	if err != nil {
		return webapp.App{}, err
	}
	return webapp.FindApp(dirs.Apps(), names[0])
}

func appList(name string, args []string) error {
	f := newAppFlags(name, "app list")
	if names, err := f.parse(args); err != nil {
		return err
	} else if len(names) > 0 {
		return fmt.Errorf("usage: %s app list", name)
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	return printApps(name, dirs)
}

func printApps(name string, dirs home.Dirs) error {
	apps, err := webapp.InstalledApps(dirs.Apps())
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		fmt.Printf("no apps installed in %s; %s app catalog lists the apps you can install\n", dirs.Apps(), name)
		return nil
	}
	fmt.Println(webapp.FormatApps(apps))
	if cfg, err := loadConfig(dirs); err == nil && cfg["app"] != "" {
		fmt.Printf("default: %s (plain \"%s\" serves it)\n", cfg["app"], name)
	}
	return nil
}

// appRun is "app run NAME [serve flags]", i.e. serve -app NAME.
func appRun(spec Spec, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && isHelpArg(args[0]) {
			return finish(spec, "app", flag.ErrHelp)
		}
		return finish(spec, "app", usageError{errors.New("app run needs the app's NAME")})
	}
	spec.Args = append([]string{"-app", args[0]}, args[1:]...)
	return Serve(spec)
}

func appInstall(name string, args []string) error {
	f := newAppFlags(name, "app install")
	from := f.String("from", "", "the app's signed manifest URL, e.g. https://…/stable.json")
	key := f.String("key", "", "its public key, or a file holding it")
	names, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	if len(names) == 1 && *from == "" && *key == "" { // by name, from a catalog
		return installFromCatalog(name, dirs, names[0])
	}
	if len(names) > 0 || *from == "" || *key == "" {
		return usageError{fmt.Errorf("usage: %s app install NAME, or app install -from URL -key KEY", name)}
	}
	return update(name, dirs.Apps(), &webapp.Flags{From: *from, Key: *key})
}

func appUpdate(name string, args []string) error {
	f := newAppFlags(name, "app update")
	names, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	var apps []webapp.App
	if len(names) == 0 {
		if apps, err = webapp.InstalledApps(dirs.Apps()); err != nil {
			return err
		}
		if len(apps) == 0 {
			return printApps(name, dirs)
		}
	}
	for _, n := range names {
		a, err := webapp.FindApp(dirs.Apps(), n)
		if err != nil {
			return err
		}
		apps = append(apps, a)
	}
	failed := 0
	for _, a := range apps {
		if a.Key == "" {
			fmt.Fprintf(os.Stderr, "%s: installed before its key was saved; run once:\n  %s app install -from %s -key KEY\n", a.Name, name, a.Manifest)
			failed++
			continue
		}
		if err := update(name, dirs.Apps(), &webapp.Flags{From: a.Manifest, Key: a.Key}); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", a.Name, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d apps not updated", failed, len(apps))
	}
	return nil
}

// serveHint: after installing, say how to serve the app.
var serveHint = true

// update checks one app's manifest now and installs a newer version.
func update(name, appsDir string, fl *webapp.Flags) error {
	in, err := fl.Installer(myserver.VERSION, appsDir, nil)
	if err != nil {
		return err
	}
	before := in.Current()
	log.SetOutput(io.Discard) // the installer logs for the server; here the result is printed
	err = in.Update(context.Background())
	log.SetOutput(os.Stderr)
	if err != nil {
		return err
	}
	a, err := webapp.FindApp(appsDir, fl.From)
	if err != nil {
		return err
	}
	switch {
	case before == "":
		fmt.Printf("%s may use: %s\n", a.Name, folderList(a.Access))
		if serveHint {
			fmt.Printf("%s %s installed; serve it with: %s app run %s\n", a.Name, a.Current, name, a.Name)
		} else {
			fmt.Printf("%s %s installed\n", a.Name, a.Current)
		}
	case before != a.Current:
		fmt.Printf("%s %s → %s (a running %s switches at its next check, or press a)\n", a.Name, before, a.Current, name)
	default:
		fmt.Printf("%s %s is up to date\n", a.Name, a.Current)
		return nil
	}
	printNotes(a.Notes[a.Current])
	return nil
}

// printNotes indents release notes under the line they belong to.
func printNotes(notes string) {
	if notes = strings.TrimSpace(notes); notes != "" {
		fmt.Println("    " + strings.ReplaceAll(notes, "\n", "\n    "))
	}
}

func appVersions(name string, args []string) error {
	f := newAppFlags(name, "app versions")
	all := f.Bool("all", false, "also list the versions published but not installed (asks the server)")
	a, err := f.oneApp(args)
	if err != nil {
		return err
	}
	head := fmt.Sprintf("%s from %s", a.Name, a.Manifest)
	if a.Pin != "" {
		head += fmt.Sprintf(" (pinned to %s; %s app use %s latest follows updates)", a.Pin, name, a.Name)
	}
	fmt.Println(head)
	if !*all {
		for _, v := range a.Versions {
			fmt.Printf("%s %s\n", mark(v == a.Current), v)
			printNotes(a.Notes[v])
		}
		fmt.Printf("(%s app versions %s -all lists every published version)\n", name, a.Name)
		return nil
	}
	in, err := (&webapp.Flags{From: a.Manifest, Key: a.Key}).Installer(myserver.VERSION, filepath.Dir(a.Dir), nil)
	if err != nil {
		return err
	}
	vs, err := in.Available(context.Background())
	if err != nil {
		return err
	}
	for _, m := range vs.Versions {
		where := "published " + m.Published.Format("2006-01-02")
		if contains(a.Versions, m.Version) {
			where = "installed"
		}
		fmt.Printf("%s %-12s %s\n", mark(m.Version == a.Current), m.Version, where)
		printNotes(m.Notes)
	}
	return nil
}

func mark(current bool) string {
	if current {
		return "*"
	}
	return " "
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// appUse is "app use NAME VERSION|latest": serve that version from now on
// (installing it if needed) and stop updating, or follow updates again.
func appUse(name string, args []string) error {
	f := newAppFlags(name, "app use")
	words, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(words) != 2 {
		return fmt.Errorf("usage: %s app use NAME VERSION|latest", name)
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	a, err := webapp.FindApp(dirs.Apps(), words[0])
	if err != nil {
		return err
	}
	if a.Key == "" {
		return fmt.Errorf("%s was installed before its key was saved; run once:\n  %s app install -from %s -key KEY", a.Name, name, a.Manifest)
	}
	in, err := (&webapp.Flags{From: a.Manifest, Key: a.Key}).Installer(myserver.VERSION, dirs.Apps(), nil)
	if err != nil {
		return err
	}
	log.SetOutput(io.Discard) // the installer logs for the server; here the result is printed
	err = in.Use(context.Background(), words[1])
	log.SetOutput(os.Stderr)
	if err != nil {
		return err
	}
	st := in.Status()
	if st.Pinned != "" {
		fmt.Printf("%s now serves %s and won't update (%s app use %s latest follows updates again)\n", st.Name, st.Current, name, st.Name)
	} else {
		fmt.Printf("%s follows updates again; serving %s\n", st.Name, st.Current)
	}
	printNotes(st.Notes[st.Current])
	if n := tellRunning(dirs, a.Manifest); n > 0 {
		fmt.Printf("told the running %s to switch\n", name)
	}
	return nil
}

// tellRunning asks the running copies that serve this app to pick up a change
// right away (otherwise they do at their next check); it returns how many did.
func tellRunning(dirs home.Dirs, manifest string) int {
	files, _ := filepath.Glob(filepath.Join(dirs.Root, "run", "*.json"))
	n := 0
	for _, file := range files {
		in := running(file)
		if in == nil || in.Source != "app:"+manifest {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(in.Port)+"/cmd/app", strings.NewReader(`{"refresh":true}`))
		if err != nil {
			continue
		}
		if u, err := url.Parse(in.URL); err == nil && u.Query().Get("token") != "" {
			req.Header.Set("Authorization", "Bearer "+u.Query().Get("token"))
		}
		c := http.Client{Timeout: 5 * time.Second}
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				n++
			}
		}
	}
	return n
}

func appRemove(name string, args []string) error {
	f := newAppFlags(name, "app remove")
	yes := f.Bool("y", false, "don't ask")
	a, err := f.oneApp(args)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Printf("remove %s (installed: %s) from %s? [y/N] ", a.Name, strings.Join(a.Versions, ", "), a.Dir)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return errors.New("not removed")
		}
	}
	if err := os.RemoveAll(a.Dir); err != nil {
		return fmt.Errorf("%v (is a %s still serving it?)", err, name)
	}
	fmt.Printf("%s removed\n", a.Name)
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	if cfg, err := loadConfig(dirs); err == nil && (cfg["app"] == a.Name || cfg["app"] == a.Manifest) {
		delete(cfg, "app")
		if err := cfg.save(dirs); err != nil {
			return err
		}
		fmt.Printf("it was the default app; plain %s serves files again\n", name)
	}
	return nil
}

// appDefault shows or sets the app that plain "myweb" serves (config "app").
func appDefault(name string, args []string) error {
	f := newAppFlags(name, "app default")
	clearApp := f.Bool("clear", false, "serve files again, no app")
	names, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(dirs)
	if err != nil {
		return err
	}
	var done string
	switch {
	case *clearApp && len(names) == 0:
		delete(cfg, "app")
		done = fmt.Sprintf("no default app; plain \"%s\" serves files", name)
	case len(names) == 1 && !*clearApp:
		a, err := webapp.FindApp(dirs.Apps(), names[0])
		if err != nil {
			return err
		}
		cfg["app"] = names[0] // as given: a name, or the manifest URL when names clash
		done = fmt.Sprintf("plain \"%s\" now serves %s %s", name, a.Name, a.Current)
	case len(names) == 0:
		if cfg["app"] == "" {
			fmt.Printf("no default app; set one with: %s app default NAME\n", name)
		} else {
			fmt.Println(cfg["app"])
		}
		return nil
	default:
		return fmt.Errorf("usage: %s app default [NAME | -clear]", name)
	}
	if err := cfg.save(dirs); err != nil {
		return err
	}
	fmt.Println(done)
	return nil
}
