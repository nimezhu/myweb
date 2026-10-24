package webapp

// Which folders of the data folder an app may use. The publisher declares
// the app's own in the signed manifest (myweb-publish build -folder); the
// user grants more (myweb app allow). An app whose manifest declares none
// gets /<name>.

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Folder is a folder of the data folder, e.g. /rx-graphic-editor.
type Folder struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

func (f Folder) String() string {
	if f.ReadOnly {
		return f.Path + " (read only)"
	}
	return f.Path
}

// CleanFolder checks a folder path and returns it cleaned: absolute, inside
// the data folder, and not the server's own .myweb.
func CleanFolder(p string) (string, error) {
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("folder %q: use / to separate folders", p)
	}
	c := path.Clean("/" + p)
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("folder %q may not contain ..", p)
		}
	}
	if c == "/.myweb" || strings.HasPrefix(c, "/.myweb/") {
		return "", fmt.Errorf("folder %q is the server's own", p)
	}
	return c, nil
}

// mergeFolders combines lists: one entry per path, writable if any is.
func mergeFolders(lists ...[]Folder) []Folder {
	byPath := map[string]*Folder{}
	var order []string
	for _, l := range lists {
		for _, f := range l {
			if g, ok := byPath[f.Path]; ok {
				g.ReadOnly = g.ReadOnly && f.ReadOnly
				continue
			}
			f := f
			byPath[f.Path] = &f
			order = append(order, f.Path)
		}
	}
	out := make([]Folder, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out
}

// Declared are the folders the current version's publisher asks for.
func (in *Installer) Declared() []Folder {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.declaredLocked()
}

func (in *Installer) declaredLocked() []Folder {
	if fs, ok := in.state.Folders[in.state.Current]; ok && len(fs) > 0 {
		return append([]Folder(nil), fs...)
	}
	if in.state.Name == "" {
		return nil
	}
	return []Folder{{Path: "/" + in.state.Name}}
}

// Grants are the folders the user gave the app.
func (in *Installer) Grants() []Folder {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]Folder(nil), in.state.Grants...)
}

// Access is everything the app may use now: declared and granted.
func (in *Installer) Access() []Folder {
	in.mu.Lock()
	defer in.mu.Unlock()
	return mergeFolders(in.declaredLocked(), in.state.Grants)
}

// Grant lets the app use a folder (replacing an earlier grant of it).
func (in *Installer) Grant(f Folder) error {
	p, err := CleanFolder(f.Path)
	if err != nil {
		return err
	}
	f.Path = p
	in.mu.Lock()
	defer in.mu.Unlock()
	in.loadState()
	var gs []Folder
	for _, g := range in.state.Grants {
		if g.Path != p {
			gs = append(gs, g)
		}
	}
	in.state.Grants = append(gs, f)
	sort.Slice(in.state.Grants, func(i, j int) bool { return in.state.Grants[i].Path < in.state.Grants[j].Path })
	return in.saveStateLocked()
}

// Revoke takes a granted folder back.
func (in *Installer) Revoke(p string) error {
	c, err := CleanFolder(p)
	if err != nil {
		return err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.loadState()
	var gs []Folder
	for _, g := range in.state.Grants {
		if g.Path != c {
			gs = append(gs, g)
		}
	}
	if len(gs) == len(in.state.Grants) {
		return fmt.Errorf("%s was not granted (folders the app declares can't be taken back; remove the app instead)", c)
	}
	in.state.Grants = gs
	return in.saveStateLocked()
}

// Port is the app's own port, 0 until SetPort.
func (in *Installer) Port() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state.Port
}

// SetPort keeps the app's own port, so its address (and what the browser
// stores for it) stays the same from one start to the next.
func (in *Installer) SetPort(port int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.loadState()
	if in.state.Port == port {
		return nil
	}
	in.state.Port = port
	return in.saveStateLocked()
}
