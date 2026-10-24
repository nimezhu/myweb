package cli

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// registerAppsCmd serves POST /cmd/apps, the file manager's Apps panel:
// every installed app, where this server serves it, its folders; and what
// "app update/allow/deny/remove" do:
//
//	{}                                       the list
//	{"name":"x","update":true}               check for an update now
//	{"name":"x","allow":"/photos","write":true}
//	{"name":"x","deny":"/photos"}
//	{"name":"x","remove":true}
//
// Apps' pages can't use it; changes are for this server's own pages and,
// multi-user, for admins.
func registerAppsCmd(srv *myserver.Server, spec Spec, set *appSet) {
	srv.RegisterCmd("apps", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
		if req.User.IsApp() {
			return nil, myserver.Errorf(http.StatusForbidden, "apps can't manage apps")
		}
		var p struct {
			Name   string `json:"name"`
			Update bool   `json:"update"`
			Allow  string `json:"allow"`
			Write  bool   `json:"write"`
			Deny   string `json:"deny"`
			Remove bool   `json:"remove"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		change := p.Update || p.Allow != "" || p.Deny != "" || p.Remove
		if change {
			if req.User != nil && req.User.Role != "admin" {
				return nil, myserver.Errorf(http.StatusForbidden, "only admins can change apps")
			}
			if o := req.HTTP.Header.Get("Origin"); o != "" { // not -cors sites either
				if u, err := url.Parse(o); err != nil || u.Host != req.HTTP.Host {
					return nil, myserver.Errorf(http.StatusForbidden, "apps can be changed only from this server's own pages")
				}
			}
			a, err := webapp.FindApp(set.dirs.Apps(), p.Name)
			if err != nil {
				return nil, myserver.Errorf(http.StatusNotFound, "%v", err)
			}
			if err := set.change(ctx, spec, a, p.Update, p.Allow, p.Write, p.Deny, p.Remove); err != nil {
				return nil, myserver.Errorf(http.StatusBadRequest, "%v", err)
			}
		}
		return set.rows()
	}, myserver.Describe(`installed apps, where they are served and their folders: {} lists them; with {"name":…}: {"update":true}, {"allow":"/photos","write":false}, {"deny":"/photos"}, {"remove":true}`))
}

// appRow is one app of the Apps panel.
type appRow struct {
	Name     string          `json:"name"`
	Current  string          `json:"current"`
	Latest   string          `json:"latest,omitempty"` // once checked, if this server keeps it updated
	Status   string          `json:"status,omitempty"`
	Pinned   string          `json:"pinned,omitempty"`
	Versions []string        `json:"versions"`
	Access   []webapp.Folder `json:"access"`
	Grants   []webapp.Folder `json:"grants,omitempty"`
	URL      string          `json:"url,omitempty"`  // where this server serves it
	Main     bool            `json:"main,omitempty"` // the one at "/"
	Default  bool            `json:"default,omitempty"`
	NoKey    bool            `json:"noKey,omitempty"` // installed before keys were kept: can't be updated
}

func (s *appSet) rows() ([]appRow, error) {
	installed, err := webapp.InstalledApps(s.dirs.Apps())
	if err != nil {
		return nil, err
	}
	def := ""
	if cfg, err := loadConfig(s.dirs); err == nil {
		def = cfg["app"]
	}
	out := []appRow{}
	for _, a := range installed {
		r := appRow{Name: a.Name, Current: a.Current, Pinned: a.Pin, Versions: a.Versions, Access: a.Access, Grants: a.Grants,
			Default: def != "" && (def == a.Name || def == a.Manifest), NoKey: a.Key == ""}
		if in := s.byName(a.Name); in != nil {
			st := in.Status()
			r.Latest, r.Status, r.Access, r.Grants = st.Latest, st.Status, st.Access, st.Grants
			r.Main = s.main != nil && in == s.main
		}
		if u := s.srv.MountURL(a.Name); u != "" {
			r.URL = u
		} else if r.Main {
			r.URL = s.srv.URL() + "/"
		}
		if r.Versions == nil {
			r.Versions = []string{}
		}
		out = append(out, r)
	}
	return out, nil
}

// change does one of the panel's changes to the installed app a.
func (s *appSet) change(ctx context.Context, spec Spec, a webapp.App, update bool, allow string, write bool, deny string, remove bool) error {
	if remove {
		if s.main != nil && s.main.Status().Manifest == a.Manifest {
			return errors.New(a.Name + " is the app this server serves at /; stop it and remove it from the command line")
		}
		if err := os.RemoveAll(a.Dir); err != nil {
			return err
		}
		s.mountAll() // stops serving it
		if cfg, err := loadConfig(s.dirs); err == nil && (cfg["app"] == a.Name || cfg["app"] == a.Manifest) {
			delete(cfg, "app")
			cfg.save(s.dirs)
		}
		go tellRescan(s.dirs) // other servers
		return nil
	}
	if a.Key == "" {
		return errors.New(a.Name + " was installed before keys were kept; install it again first")
	}
	in := s.byName(a.Name)
	served := in != nil
	if !served {
		var err error
		if in, err = (&webapp.Flags{From: a.Manifest, Key: a.Key}).Installer(myserver.VERSION, s.dirs.Apps(), nil); err != nil {
			return err
		}
	}
	switch {
	case update && served:
		in.CheckNow() // in the background; the list shows its status
		return nil
	case update:
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return in.Update(ctx)
	case allow != "":
		if err := in.Grant(webapp.Folder{Path: allow, ReadOnly: !write}); err != nil {
			return err
		}
	case deny != "":
		if err := in.Revoke(deny); err != nil {
			return err
		}
	}
	go tellRunning(s.dirs, a.Manifest) // other servers serving it
	return nil
}
