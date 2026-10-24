package webapp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// App is an app installed with -web-from, as recorded in its state.json.
type App struct {
	Name     string
	Dir      string // its folder; everything installed for it is inside
	Manifest string
	Key      string // "" if installed before keys were saved
	Current  string
	Pin      string            // the version chosen with "app use", which stops updates
	Versions []string          // newest first
	Notes    map[string]string // release notes by version, when published
	Access   []Folder          // the folders it may use: declared and granted
	Grants   []Folder          // those the user granted
	Port     int               // its own port, once it has been served beside other apps
}

// InstalledApps lists the apps installed under dir (e.g. ~/.myweb/apps), by name.
func InstalledApps(dir string) ([]App, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []App
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		in := &Installer{home: filepath.Join(dir, e.Name())}
		in.loadState()
		if in.state.Name == "" || in.state.Current == "" {
			continue
		}
		apps = append(apps, App{Name: in.state.Name, Dir: in.home, Manifest: in.state.Manifest, Key: in.state.Key,
			Current: in.state.Current, Pin: in.state.Pin, Versions: in.Installed(), Notes: in.state.Notes,
			Access: in.Access(), Grants: in.Grants(), Port: in.state.Port})
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].Manifest < apps[j].Manifest
	})
	return apps, nil
}

// FindApp returns the installed app with this name, or its manifest URL.
func FindApp(dir, name string) (App, error) {
	apps, err := InstalledApps(dir)
	if err != nil {
		return App{}, err
	}
	var found []App
	for _, a := range apps {
		if a.Name == name || a.Manifest == name {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		var urls []string
		for _, a := range found {
			urls = append(urls, "  "+a.Manifest)
		}
		return App{}, fmt.Errorf("%s is installed from several manifests; give -app one of them:\n%s", name, strings.Join(urls, "\n"))
	case len(apps) == 0:
		return App{}, fmt.Errorf("no apps are installed in %s yet; install one with -web-from URL -web-key KEY", dir)
	}
	return App{}, fmt.Errorf("%s is not installed; installed:\n%s", name, FormatApps(apps))
}

// FormatApps renders apps one per line for -apps.
func FormatApps(apps []App) string {
	var b strings.Builder
	for _, a := range apps {
		fmt.Fprintf(&b, "  %-16s %-10s %s", a.Name, a.Current, a.Manifest)
		if len(a.Versions) > 1 {
			fmt.Fprintf(&b, "  (installed: %s)", strings.Join(a.Versions, ", "))
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
