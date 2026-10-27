package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// Home's Settings and first run change the tray app's settings (the folder it
// serves, start at login, updates), which belong to the tray, not the server.
// The tray starts its server with DesktopEnv set; that server then serves
// POST /cmd/desktop and passes each request on to the tray, through the local
// address the tray listens on (<home>/run/desktop.addr): a line
// "json {request}", answered with a line of JSON.

// DesktopEnv is set (to "1") for the server the tray app starts.
const DesktopEnv = "MYWEB_DESKTOP"

// DesktopState is the tray app's settings and state, as Home shows them.
type DesktopState struct {
	Folder      string `json:"folder"`      // the data folder served
	FolderShort string `json:"folderShort"` // the same, as ~/…
	Login       bool   `json:"login"`       // starts at login
	AutoUpdate  bool   `json:"autoUpdate"`  // installs myweb updates by itself
	FirstRun    bool   `json:"firstRun"`    // a new install, not yet welcomed
	Version     string `json:"version"`
	Newer       string `json:"newer,omitempty"` // a newer myweb, once found
	Ready       string `json:"ready,omitempty"` // downloaded: myweb runs it from its next start
	Updating    bool   `json:"updating,omitempty"`
	CantUpdate  string `json:"cantUpdate,omitempty"` // why it can't update itself
	Note        string `json:"note,omitempty"`       // e.g. "checking for updates…"
	OS          string `json:"os"`                   // runtime.GOOS: Home names Finder or Explorer
}

// DesktopRequest is one of Home's requests; an empty one reads the state.
type DesktopRequest struct {
	Pick       bool   `json:"pick,omitempty"`   // ask for a folder with the system's dialog (not applied)
	Folder     string `json:"folder,omitempty"` // serve this folder: the server restarts there, and Home opens on it
	Login      *bool  `json:"login,omitempty"`
	AutoUpdate *bool  `json:"autoUpdate,omitempty"`
	Welcomed   bool   `json:"welcomed,omitempty"` // the first run is done
	Show       string `json:"show,omitempty"`     // "folder" or "log": open it on the desktop
	Check      bool   `json:"check,omitempty"`    // look for a myweb update now
	Restart    bool   `json:"restart,omitempty"`  // restart into the update downloaded
}

// DesktopReply is the tray's answer.
type DesktopReply struct {
	DesktopState
	Picked      string `json:"picked,omitempty"` // the folder chosen, for Pick ("" if cancelled)
	PickedShort string `json:"pickedShort,omitempty"`
	Moving      bool   `json:"moving,omitempty"` // the server is restarting on Folder
	Error       string `json:"error,omitempty"`
}

// DesktopAddrFile is where the running tray app records its address.
func DesktopAddrFile(dirs home.Dirs) string { return filepath.Join(dirs.Root, "run", "desktop.addr") }

// askDesktop sends req to the tray app and returns its answer.
func askDesktop(dirs home.Dirs, req DesktopRequest) (*DesktopReply, error) {
	b, err := os.ReadFile(DesktopAddrFile(dirs))
	if err != nil {
		return nil, errors.New("the myweb app isn't running")
	}
	c, err := net.DialTimeout("tcp", strings.TrimSpace(string(b)), time.Second)
	if err != nil {
		return nil, errors.New("the myweb app isn't answering")
	}
	defer c.Close()
	wait := 15 * time.Second
	if req.Pick { // someone is choosing in a dialog
		wait = 10 * time.Minute
	}
	c.SetDeadline(time.Now().Add(wait))
	body, _ := json.Marshal(req)
	if _, err := c.Write(append(append([]byte("json "), body...), '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return nil, errors.New("the myweb app didn't answer")
	}
	var out DesktopReply
	if err := json.Unmarshal(line, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// registerDesktopCmd serves POST /cmd/desktop when the tray app started this
// server; elsewhere there is no such command, and Home leaves its settings out.
func registerDesktopCmd(srv *myserver.Server, dirs home.Dirs) {
	if os.Getenv(DesktopEnv) != "1" {
		return
	}
	srv.RegisterCmd("desktop", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
		if req.User.IsApp() {
			return nil, myserver.Errorf(http.StatusForbidden, "apps can't change myweb's settings")
		}
		if req.User != nil && req.User.Role != "admin" {
			return nil, myserver.Errorf(http.StatusForbidden, "only admins can change myweb's settings")
		}
		// like the stores: only from this server's own pages
		if o := req.HTTP.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != req.HTTP.Host {
				return nil, myserver.Errorf(http.StatusForbidden, "settings can be changed only from this server's own pages")
			}
		}
		var p DesktopRequest
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		out, err := askDesktop(dirs, p)
		if err != nil {
			return nil, myserver.Errorf(http.StatusServiceUnavailable, "%v", err)
		}
		if out.Error != "" {
			return nil, myserver.Errorf(http.StatusBadRequest, "%s", out.Error)
		}
		return out, nil
	}, myserver.Describe(`the myweb app's settings (when it started this server): {} reads them; {"folder":DIR}, {"login":BOOL}, {"autoUpdate":BOOL}, {"welcomed":true}, {"pick":true}, {"show":"folder"|"log"}, {"check":true}, {"restart":true}`))
}
