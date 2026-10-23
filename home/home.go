// Package home locates an app's own folder in the user's home: ~/.<name>
// (e.g. ~/.myweb), or $<NAME>_HOME (e.g. MYWEB_HOME) when set; the -home flag
// overrides both. It holds the
// default data folder and the installed web apps:
//
//	~/.myweb/data   served when no data folder is given
//	~/.myweb/apps   web apps installed with -web-from
package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Dirs is one app's folder.
type Dirs struct{ Root string }

// For returns the folder for name: $<NAME>_HOME if set, else ~/.<name>.
func For(name string) (Dirs, error) {
	if v := os.Getenv(EnvVar(name)); v != "" {
		abs, err := filepath.Abs(v)
		return Dirs{abs}, err
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return Dirs{}, errors.New("cannot find your home folder; give the data folder explicitly")
	}
	return Dirs{filepath.Join(h, "."+name)}, nil
}

// EnvVar is the variable that relocates the folder, e.g. MYWEB_HOME.
func EnvVar(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_HOME"
}

// Data is the default data folder.
func (d Dirs) Data() string { return filepath.Join(d.Root, "data") }

// Apps is where -web-from installs web apps.
func (d Dirs) Apps() string { return filepath.Join(d.Root, "apps") }

// EnsureData creates the data folder if needed; created reports whether it
// was just made (worth telling the user where their files go).
func (d Dirs) EnsureData() (path string, created bool, err error) {
	path = d.Data()
	if fi, err := os.Stat(path); err == nil {
		if !fi.IsDir() {
			return path, false, errors.New(path + " exists but is not a folder")
		}
		return path, false, nil
	}
	return path, true, os.MkdirAll(path, 0o755)
}
