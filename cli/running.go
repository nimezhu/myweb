package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/webapp"
)

// The servers that are running, from their records in <home>/run:
//
//	status [-json]          list them
//	open [WHICH]            open one in the browser
//	stop [WHICH | -all]     stop one, or all
//
// WHICH is the number status shows, a pid, a port, an app's name or a data
// folder; it may be left out when only one server runs.

// openBrowser is replaced in tests.
var openBrowser = console.OpenBrowser

// server is a running instance with what status shows of it.
type server struct {
	*instance
	file  string
	App   string `json:"app,omitempty"` // an installed app's name
	Serve string `json:"serves"`        // files, an app, or a -web folder
}

// runningServers lists the servers that answer, oldest first.
func runningServers(dirs home.Dirs) []server {
	files, _ := filepath.Glob(filepath.Join(dirs.Root, "run", "*.json"))
	var out []server
	for _, f := range files {
		in := running(f)
		if in == nil {
			continue
		}
		s := server{instance: in, file: f, Serve: "files"}
		switch kind, v, _ := strings.Cut(in.Source, ":"); kind {
		case "app":
			s.Serve = v
			if a, err := webapp.FindApp(dirs.Apps(), v); err == nil {
				s.App, s.Serve = a.Name, "app "+a.Name
			}
		case "web":
			s.Serve = "web " + v
		}
		for _, a := range in.Apps {
			s.Serve += ", " + a.Name
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// address is the server's URL without its token.
func (s server) address() string {
	if s.URL == "" {
		return s.base() + "/_myweb/"
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// appURL is where the server serves app on a port of its own, if it does.
func (s server) appURL(app string) string {
	for _, a := range s.Apps {
		if a.Name == app {
			if t := s.token(); t != "" {
				return a.URL + "?token=" + t
			}
			return a.URL
		}
	}
	return ""
}

// token is the access token the server was started with, if any.
func (s server) token() string {
	if u, err := url.Parse(s.URL); err == nil {
		return u.Query().Get("token")
	}
	return ""
}

func statusCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "status")
	asJSON := fl.Bool("json", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{errors.New("status takes no arguments")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	list := runningServers(dirs)
	if *asJSON {
		type row struct {
			PID     int       `json:"pid"`
			Port    int       `json:"port"`
			URL     string    `json:"url"`
			Root    string    `json:"root"`
			Serves  string    `json:"serves"`
			App     string    `json:"app,omitempty"`
			Version string    `json:"version,omitempty"`
			Started time.Time `json:"started"`
		}
		rows := []row{}
		for _, s := range list {
			rows = append(rows, row{s.PID, s.Port, s.address(), s.Root, s.Serve, s.App, s.Version, s.Started})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(list) == 0 {
		fmt.Fprintf(w, "no %s is running\n", spec.Name)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tPID\tADDRESS\tFOLDER\tSERVES\tVERSION\tUP")
	for i, s := range list {
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n", i+1, s.PID, s.address(), tildePath(s.Root), s.Serve, s.Version, uptime(s.Started))
		for _, a := range s.Apps {
			fmt.Fprintf(tw, "\t\t%s\t\tapp %s\t\t\n", a.URL, a.Name)
		}
	}
	return tw.Flush()
}

func openCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "open")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{errors.New("open takes one server")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	s, err := pickServer(spec.Name, runningServers(dirs), pos, "open")
	if err != nil {
		return err
	}
	target := s.URL
	if len(pos) == 1 && s.appURL(pos[0]) != "" { // an app it serves on its own port
		target = s.appURL(pos[0])
	}
	if target == "" {
		target = s.address()
	}
	shown, _ := url.Parse(target)
	if shown != nil {
		shown.RawQuery = ""
		fmt.Fprintf(w, "opening %s\n", shown)
	}
	return openBrowser(target)
}

func stopCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "stop")
	all := fl.Bool("all", false, "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (*all && len(pos) > 0) {
		return usageError{errors.New("stop takes one server, or -all")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	list := runningServers(dirs)
	if !*all {
		s, err := pickServer(spec.Name, list, pos, "stop")
		if err != nil {
			return err
		}
		list = []server{s}
	} else if len(list) == 0 {
		fmt.Fprintf(w, "no %s is running\n", spec.Name)
		return nil
	}
	var failed []string
	for _, s := range list {
		if err := stopServer(s); err != nil {
			failed = append(failed, fmt.Sprintf("pid %d: %v", s.PID, err))
			continue
		}
		fmt.Fprintf(w, "stopped %s (pid %d, %s)\n", s.address(), s.PID, tildePath(s.Root))
	}
	if len(failed) > 0 {
		return errors.New("could not stop " + strings.Join(failed, "; "))
	}
	return nil
}

// stopServer asks the server to stop (as its own page does), else signals
// it, and waits until it is gone.
func stopServer(s server) error {
	req, _ := http.NewRequest(http.MethodPost, s.base()+"/cmd/stopServer", nil)
	if t := s.token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	c := s.client(3 * time.Second)
	asked := false
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		asked = resp.StatusCode == http.StatusOK
	}
	if !asked { // e.g. a team server, whose stop needs an admin: the owner may signal it
		p, err := os.FindProcess(s.PID)
		if err != nil {
			return err
		}
		if runtime.GOOS == "windows" {
			err = p.Kill()
		} else {
			err = p.Signal(syscall.SIGTERM)
		}
		if err != nil {
			return err
		}
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if running(s.file) == nil {
			return nil
		}
	}
	return errors.New("still running after 10s")
}

// pickServer finds the one server args name (or the only one running).
func pickServer(name string, list []server, args []string, verb string) (server, error) {
	if len(list) == 0 {
		return server{}, fmt.Errorf("no %s is running", name)
	}
	if len(args) == 0 {
		if len(list) == 1 {
			return list[0], nil
		}
		return server{}, usageError{fmt.Errorf("%d servers are running; say which: %s %s NUMBER (see %s status)", len(list), name, verb, name)}
	}
	want := args[0]
	if n, err := strconv.Atoi(want); err == nil {
		if n >= 1 && n <= len(list) {
			return list[n-1], nil
		}
		for _, s := range list {
			if s.PID == n || s.Port == n {
				return s, nil
			}
		}
		return server{}, fmt.Errorf("no running server %d (see %s status)", n, name)
	}
	abs, _ := filepath.Abs(expandHome(want))
	var found []server
	for _, s := range list {
		if s.App == want || s.appURL(want) != "" || s.Root == abs {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return server{}, fmt.Errorf("no running server for %q (see %s status)", want, name)
	case 1:
		return found[0], nil
	}
	return server{}, usageError{fmt.Errorf("%d servers match %q; say which by number (see %s status)", len(found), want, name)}
}

func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && (p == h || strings.HasPrefix(p, h+string(filepath.Separator))) {
		return "~" + p[len(h):]
	}
	return p
}

func uptime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}
