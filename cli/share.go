package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// "share" makes links to a folder or file of a running server's data
// folder, that open without the token (see myserver/share.go):
//
//	share PATH [-upload] [-days N] [-on WHICH]   a link (-upload: a drop box)
//	share list [-on WHICH]
//	share revoke ID [-on WHICH]
//
// PATH is in the data folder (/photos), or a file or folder on disk inside it.

func shareCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "share")
	upload := fl.Bool("upload", false, "")
	days := fl.Int("days", 7, "")
	on := fl.String("on", "", "")
	pos, err := fl.parse(args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{errors.New("share takes a PATH, list or revoke ID")}
	}
	dirs, err := homeFor(spec.Name, *fl.home)
	if err != nil {
		return err
	}
	var which []string
	if *on != "" {
		which = []string{*on}
	}
	s, err := pickServer(spec.Name, runningServers(dirs), which, "share … -on")
	if err != nil {
		if len(runningServers(dirs)) == 0 {
			return fmt.Errorf("no %s is running: share links are served by a running server (start %s first)", spec.Name, spec.Name)
		}
		return err
	}
	switch {
	case pos[0] == "list" && len(pos) == 1:
		var list []struct {
			ID      string    `json:"id"`
			Path    string    `json:"path"`
			Upload  bool      `json:"upload"`
			Expires time.Time `json:"expires"`
			By      string    `json:"by"`
		}
		if err := callCmd(s, "shares", nil, &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(w, "no share links")
			return nil
		}
		t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(t, "ID\tPATH\tKIND\tEXPIRES")
		for _, sh := range list {
			kind := "read"
			if sh.Upload {
				kind = "upload"
			}
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", sh.ID, sh.Path, kind, sh.Expires.Local().Format("2006-01-02 15:04"))
		}
		return t.Flush()
	case pos[0] == "revoke":
		if len(pos) != 2 {
			return usageError{errors.New("share revoke takes the link's ID (see share list)")}
		}
		if err := callCmd(s, "shareRevoke", map[string]string{"id": pos[1]}, nil); err != nil {
			return err
		}
		fmt.Fprintf(w, "revoked %s\n", pos[1])
		return nil
	case len(pos) > 1:
		return usageError{errors.New("share one PATH at a time")}
	}
	p, err := dataPath(s.Root, pos[0])
	if err != nil {
		return err
	}
	var res struct {
		ID      string    `json:"id"`
		URL     string    `json:"url"`
		Expires time.Time `json:"expires"`
		Local   bool      `json:"local"`
	}
	if err := callCmd(s, "shareCreate", map[string]interface{}{"path": p, "upload": *upload, "days": *days}, &res); err != nil {
		return err
	}
	kind := "opens"
	if *upload {
		kind = "takes files into"
	}
	fmt.Fprintf(w, "%s\n\nthis link %s %s until %s, without the token (%s share revoke %s ends it)\n",
		res.URL, kind, p, res.Expires.Local().Format("2 Jan 2006 15:04"), spec.Name, res.ID)
	if res.Local {
		fmt.Fprintf(w, "the server listens on this machine only: other machines reach the link with -global (or a tunnel)\n")
	}
	return nil
}

// dataPath turns arg into a path of the data folder at root: /photos as is,
// a file or folder on disk inside root as its place in it.
func dataPath(root, arg string) (string, error) {
	if abs, err := filepath.Abs(expandHome(arg)); err == nil {
		if _, err := os.Stat(abs); err == nil {
			rel, err := filepath.Rel(root, abs)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "/" + strings.TrimPrefix(filepath.ToSlash(rel), "."), nil
			}
			if !strings.HasPrefix(arg, "/") {
				return "", fmt.Errorf("%s is not in the data folder %s", arg, tildePath(root))
			}
		}
	}
	return "/" + strings.TrimLeft(filepath.ToSlash(arg), "/"), nil
}

// callCmd runs a command of a running server and decodes its result into out.
func callCmd(s server, name string, params interface{}, out interface{}) error {
	body := "{}"
	if params != nil {
		b, _ := json.Marshal(params)
		body = string(b)
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(s.Port)+"/cmd/"+name, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t := s.token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return fmt.Errorf("%s: %s", name, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		if r.Error == "" {
			r.Error = resp.Status
		}
		return errors.New(r.Error)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}
