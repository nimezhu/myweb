package cli

import (
	"context"
	"log"
	"sort"
	"sync"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// appSet is the apps one server serves: the one at "/" (main, may be nil)
// and every other installed app on a port of its own (unless -other-apps
// is off), each kept up to date like main.
type appSet struct {
	dirs  home.Dirs
	root  string // the data folder
	srv   *myserver.Server
	main  *webapp.Installer
	flags webapp.Flags // -web-check, -no-update for the others
	ctx   context.Context
	on    bool // serve the others (-other-apps)

	mu       sync.Mutex
	others   map[string]*otherApp // by name
	onChange func()               // after mounting or unmounting
}

type otherApp struct {
	in     *webapp.Installer
	port   int
	cancel context.CancelFunc
}

// mountAll serves every installed app that isn't served yet, and stops
// serving those no longer installed.
func (s *appSet) mountAll() {
	if !s.on {
		return
	}
	installed, err := webapp.InstalledApps(s.dirs.Apps())
	if err != nil {
		log.Printf("apps: %v", err)
		return
	}
	// apps that had a port first, so they get it back
	sort.SliceStable(installed, func(i, j int) bool { return installed[i].Port != 0 && installed[j].Port == 0 })
	mainManifest := ""
	if s.main != nil {
		mainManifest = s.main.Status().Manifest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.others == nil {
		s.others = map[string]*otherApp{}
	}
	changed := false
	present := map[string]bool{}
	for _, a := range installed {
		if a.Manifest == mainManifest || a.Key == "" {
			continue
		}
		present[a.Name] = true
		if s.others[a.Name] != nil {
			continue
		}
		fl := s.flags
		fl.App, fl.From, fl.Key, fl.Version = "", a.Manifest, a.Key, ""
		var in *webapp.Installer
		in, err := fl.Installer(myserver.VERSION, s.dirs.Apps(), func(n, v string) {
			makeFolders(in, s.root)
			s.srv.Broadcast(map[string]string{"type": "app-updated", "name": n, "version": v, "notes": in.Status().Notes[v]})
		})
		if err != nil {
			log.Printf("%s: %v", a.Name, err)
			continue
		}
		port, err := s.srv.Mount(&myserver.Mount{Access: limitApp(in, s.root), FS: in.FS(), Version: in.Current}, a.Port)
		if err != nil {
			log.Printf("%s: %v", a.Name, err)
			continue
		}
		switch {
		case a.Port == 0:
			in.SetPort(port)
		case port != a.Port:
			log.Printf("%s: its port %d is taken; serving it at %d for now (what the browser keeps for it is at %d)", a.Name, a.Port, port, a.Port)
		}
		ctx, cancel := context.WithCancel(s.ctx)
		in.Start(ctx)
		s.others[a.Name] = &otherApp{in, port, cancel}
		changed = true
	}
	for name, o := range s.others {
		if !present[name] {
			o.cancel()
			s.srv.Unmount(name)
			delete(s.others, name)
			changed = true
		}
	}
	if changed && s.onChange != nil {
		go s.onChange()
	}
}

// byName is the installer of a served app ("" names main).
func (s *appSet) byName(name string) *webapp.Installer {
	if s.main != nil && (name == "" || s.main.Status().Name == name) {
		return s.main
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.others[name]; o != nil {
		return o.in
	}
	return nil
}

// url is where a served app is, "" if this server doesn't serve it.
func (s *appSet) url(name string) string {
	if s.main != nil && s.main.Status().Name == name {
		return s.srv.LoginURL()
	}
	return s.srv.MountURL(name)
}

// served lists the apps on ports of their own, for the run record.
func (s *appSet) served() []servedApp {
	var out []servedApp
	for _, m := range s.srv.Mounted() {
		out = append(out, servedApp{Name: m.Name, URL: m.URL})
	}
	return out
}
