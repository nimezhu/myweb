package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// Catalogs: signed lists of apps that can be installed by name.
//
//	app catalog [WORD] [-refresh]       the apps of every catalog
//	app catalog add URL -key KEY        read another catalog too
//	app catalog remove URL
//	app install NAME                    install an app from a catalog
//
// "app run NAME" and "-app NAME" install a catalog app that isn't installed.

func catalogsFor(dirs home.Dirs) *webapp.Catalogs {
	return &webapp.Catalogs{File: filepath.Join(dirs.Root, "catalogs.json"), Cache: filepath.Join(dirs.Root, "catalogs")}
}

func appCatalog(name string, args []string) error {
	f := newAppFlags(name, "app catalog")
	key := f.String("key", "", "")
	refresh := f.Bool("refresh", false, "")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	cats := catalogsFor(dirs)
	ctx := context.Background()
	switch {
	case len(pos) > 0 && pos[0] == "add":
		if len(pos) != 2 || *key == "" {
			return usageError{fmt.Errorf("usage: %s app catalog add URL -key KEY", name)}
		}
		cat, err := cats.Add(ctx, webapp.CatalogSource{URL: pos[1], Key: *key})
		if err != nil {
			return err
		}
		title := cat.Title
		if title == "" {
			title = pos[1]
		}
		fmt.Printf("added %s: %d apps (%s app catalog lists them)\n", title, len(cat.Apps), name)
		return nil
	case len(pos) > 0 && (pos[0] == "remove" || pos[0] == "rm"):
		if len(pos) != 2 {
			return usageError{fmt.Errorf("usage: %s app catalog remove URL", name)}
		}
		if err := cats.Remove(pos[1]); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", pos[1])
		return nil
	case len(pos) > 1:
		return usageError{fmt.Errorf("usage: %s app catalog [WORD]", name)}
	}

	apps, errs := cats.Apps(ctx, *refresh)
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	}
	word := ""
	if len(pos) == 1 {
		word = strings.ToLower(pos[0])
	}
	installed, _ := webapp.InstalledApps(dirs.Apps())
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	shown := 0
	for _, a := range apps {
		if word != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Title+" "+a.Description), word) {
			continue
		}
		if shown == 0 {
			fmt.Fprintln(tw, "NAME\tINSTALLED\tABOUT")
		}
		shown++
		state := "-"
		if in := installedFrom(installed, a); in != nil {
			state = in.Current
		}
		about := a.Title
		if a.Description != "" {
			if about != "" {
				about += ": "
			}
			about += a.Description
		}
		if len(about) > 70 {
			about = about[:67] + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Name, state, about)
	}
	tw.Flush()
	switch {
	case shown > 0:
		fmt.Printf("\ninstall one with: %s app install NAME (or just %s app run NAME)\n", name, name)
	case word != "":
		fmt.Printf("no app matches %q\n", pos[0])
	case len(errs) == 0:
		srcs, _ := cats.Sources()
		if len(srcs) == 0 {
			fmt.Printf("no catalogs; add one with: %s app catalog add URL -key KEY\n", name)
		} else {
			fmt.Println("the catalogs list no apps yet")
		}
	}
	if len(errs) > 0 {
		return errors.New("some catalogs could not be read")
	}
	return nil
}

// installedFrom is the installed app that came from the catalog entry (same
// name and manifest), if any.
func installedFrom(installed []webapp.App, a webapp.CatalogApp) *webapp.App {
	for i := range installed {
		if installed[i].Name == a.Name && installed[i].Manifest == a.Manifest {
			return &installed[i]
		}
	}
	return nil
}

// installFromCatalog installs (or updates) the catalog app called app.
func installFromCatalog(name string, dirs home.Dirs, app string) error {
	a, err := catalogsFor(dirs).Find(context.Background(), app)
	if err != nil {
		return err
	}
	installed, _ := webapp.InstalledApps(dirs.Apps())
	for _, in := range installed {
		if in.Name == a.Name && in.Manifest != a.Manifest {
			return fmt.Errorf("an app called %s is already installed from %s, not from the catalog (%s);\nremove it first: %s app remove %s", a.Name, in.Manifest, a.Manifest, name, a.Name)
		}
	}
	fmt.Printf("installing %s from %s\n", a.Name, a.Catalog)
	return update(name, dirs.Apps(), &webapp.Flags{From: a.Manifest, Key: a.Key})
}

// ensureInstalled installs a catalog app that -app names but that isn't
// installed yet.
func ensureInstalled(name string, dirs home.Dirs, app string) error {
	if _, err := webapp.FindApp(dirs.Apps(), app); err == nil || !webapp.ValidName(app) {
		return nil // installed, or a manifest URL: the usual errors follow
	}
	if _, err := catalogsFor(dirs).Find(context.Background(), app); err != nil {
		return nil
	}
	serveHint = false // it is served right after
	return installFromCatalog(name, dirs, app)
}

// registerCatalogCmd serves the catalogs at POST /cmd/catalog, for the file
// manager: {} lists the apps, {"install":NAME} installs one, {"open":NAME}
// opens it (starting "app run NAME" in the background if it isn't running).
func registerCatalogCmd(srv *myserver.Server, spec Spec, dirs home.Dirs) {
	type entry struct {
		webapp.CatalogApp
		Installed string `json:"installed,omitempty"` // its current version
		Running   bool   `json:"running,omitempty"`
	}
	list := func(ctx context.Context, refresh bool) (interface{}, error) {
		apps, errs := catalogsFor(dirs).Apps(ctx, refresh)
		installed, _ := webapp.InstalledApps(dirs.Apps())
		running := map[string]bool{}
		for _, s := range runningServers(dirs) {
			running[s.App] = true
		}
		out := struct {
			Apps   []entry  `json:"apps"`
			Errors []string `json:"errors,omitempty"`
		}{Apps: []entry{}}
		for _, a := range apps {
			e := entry{CatalogApp: a}
			if in := installedFrom(installed, a); in != nil {
				e.Installed, e.Running = in.Current, running[a.Name]
			}
			out.Apps = append(out.Apps, e)
		}
		for _, err := range errs {
			out.Errors = append(out.Errors, err.Error())
		}
		return out, nil
	}
	srv.RegisterCmd("catalog", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
		var p struct {
			Refresh bool   `json:"refresh"`
			Install string `json:"install"`
			Open    string `json:"open"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if (p.Install != "" || p.Open != "") && req.User != nil && req.User.Role != "admin" {
			return nil, myserver.Errorf(http.StatusForbidden, "only admins can install or start apps")
		}
		// browsers let any site send a "simple" POST here: installing and
		// starting apps is only for this server's own pages
		if o := req.HTTP.Header.Get("Origin"); (p.Install != "" || p.Open != "") && o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != req.HTTP.Host {
				return nil, myserver.Errorf(http.StatusForbidden, "installing and opening apps is only allowed from this server's own pages")
			}
		}
		switch {
		case p.Install != "":
			a, err := catalogsFor(dirs).Find(ctx, p.Install)
			if err != nil {
				return nil, myserver.Errorf(http.StatusNotFound, "%v", err)
			}
			in, err := (&webapp.Flags{From: a.Manifest, Key: a.Key}).Installer(myserver.VERSION, dirs.Apps(), nil)
			if err == nil {
				err = in.Update(ctx)
			}
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadGateway, "%v", err)
			}
		case p.Open != "":
			u, err := openApp(spec, dirs, p.Open)
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadGateway, "%v", err)
			}
			return map[string]string{"url": u}, nil
		}
		return list(ctx, p.Refresh)
	}, myserver.Describe(`apps from the catalogs: {} lists them; {"install":"NAME"} installs one; {"open":"NAME"} starts it if needed and returns its URL`))
}

// startApp starts "app run NAME" in the background; replaced in tests.
var startApp = func(spec Spec, dirs home.Dirs, app string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logs := filepath.Join(dirs.Root, "logs")
	os.MkdirAll(logs, 0o755)
	cmd := exec.Command(exe, "app", "run", app, "-home", dirs.Root, "-no-tui", "-no-open",
		"-log", filepath.Join(logs, app+".log"))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it if it stops while this server runs
	return nil
}

// openApp returns the URL of a server serving app, starting one if needed.
func openApp(spec Spec, dirs home.Dirs, app string) (string, error) {
	find := func() string {
		for _, s := range runningServers(dirs) {
			if s.App == app {
				if s.URL != "" {
					return s.URL
				}
				return s.address()
			}
		}
		return ""
	}
	if u := find(); u != "" {
		return u, nil
	}
	if _, err := webapp.FindApp(dirs.Apps(), app); err != nil {
		return "", err
	}
	if err := startApp(spec, dirs, app); err != nil {
		return "", err
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if u := find(); u != "" {
			return u, nil
		}
	}
	return "", fmt.Errorf("%s did not start; see %s", app, filepath.Join(dirs.Root, "logs", app+".log"))
}
