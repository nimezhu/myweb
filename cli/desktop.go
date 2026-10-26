package cli

import (
	"path/filepath"
	"time"

	"github.com/nimezhu/myweb/home"
)

// What myweb-desktop (the tray app) needs of the running servers and the
// saved defaults; the command line uses the unexported versions directly.

// Running is a server that answers, as status shows it.
type Running struct {
	PID     int
	Port    int
	Address string // its URL without the token
	Login   string // what the browser opens: signed in when it has a token
	Root    string // the data folder
	Serves  string // "files", "app NAME", "web DIR", then its other apps
	Files   bool   // serves the data folder at "/" (not an app or a -web folder)
	Apps    []RunningApp
	Started time.Time

	s server
}

// RunningApp is an app a server serves on a port of its own.
type RunningApp struct {
	Name  string
	Login string // its URL, signed in when the server has a token
}

// Servers lists program's servers that answer, oldest first.
func Servers(program string) ([]Running, error) {
	dirs, err := home.For(program)
	if err != nil {
		return nil, err
	}
	var out []Running
	for _, s := range runningServers(dirs) {
		r := Running{PID: s.PID, Port: s.Port, Address: s.address(), Login: s.URL, Root: s.Root,
			Serves: s.Serve, Files: s.Source == "files", Started: s.Started, s: s}
		if r.Login == "" {
			r.Login = r.Address
		}
		for _, a := range s.Apps {
			r.Apps = append(r.Apps, RunningApp{Name: a.Name, Login: s.appURL(a.Name)})
		}
		out = append(out, r)
	}
	return out, nil
}

// Stop asks the server to stop and waits until it has.
func (r Running) Stop() error { return stopServer(r.s) }

// DataFolder is the folder program serves when none is given: the saved
// default ("config set d DIR"), else <home>/data.
func DataFolder(program string) (string, error) {
	dirs, err := home.For(program)
	if err != nil {
		return "", err
	}
	cfg, err := loadConfig(dirs)
	if err != nil {
		return "", err
	}
	if d := cfg["d"]; d != "" {
		return filepath.Abs(expandHome(d))
	}
	return dirs.Data(), nil
}
