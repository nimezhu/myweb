package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
)

// "share" makes links to a folder or file of a running server's data
// folder, that open without the token (see myserver/share.go):
//
//	share PATH [-upload [-max-mb N] [-max-files N]] [-days N] [-password]
//	           [-max-downloads N] [-on WHICH]    a link (-upload: a drop box)
//	share list [-on WHICH]
//	share url ID [-on WHICH]                     the link again
//	share revoke ID [-on WHICH]
//
// PATH is in the data folder (/photos), or a file or folder on disk inside it.

func shareCmd(spec Spec, args []string, w io.Writer) error {
	fl := newAppFlags(spec.Name, "share")
	upload := fl.Bool("upload", false, "")
	days := fl.Int("days", 7, "")
	maxMB := fl.Int64("max-mb", 0, "")
	maxFiles := fl.Int("max-files", 0, "")
	password := fl.Bool("password", false, "")
	maxDownloads := fl.Int("max-downloads", 0, "")
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
			Bytes   int64     `json:"bytes"`
			Files   int       `json:"files"`
			Max     int64     `json:"maxBytes"`
			Down    int       `json:"downloads"`
			MaxDown int       `json:"maxDownloads"`
			Pass    bool      `json:"password"`
		}
		if err := callCmd(s, "shares", nil, &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(w, "no share links")
			return nil
		}
		t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(t, "ID\tPATH\tKIND\tEXPIRES\tUSED")
		for _, sh := range list {
			kind := "read"
			used := fmt.Sprintf("%d download%s", sh.Down, plural(sh.Down))
			if sh.MaxDown > 0 {
				used = fmt.Sprintf("%d of %d downloads", sh.Down, sh.MaxDown)
			}
			if sh.Upload {
				kind = "upload"
				used = fmt.Sprintf("%d file%s, %d of %d MB", sh.Files, plural(sh.Files), (sh.Bytes+1<<20-1)>>20, sh.Max>>20)
			}
			if sh.Pass {
				kind += ", password"
			}
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", sh.ID, sh.Path, kind, sh.Expires.Local().Format("2006-01-02 15:04"), used)
		}
		return t.Flush()
	case pos[0] == "url":
		if len(pos) != 2 {
			return usageError{errors.New("share url takes the link's ID (see share list)")}
		}
		var list []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if err := callCmd(s, "shares", nil, &list); err != nil {
			return err
		}
		for _, sh := range list {
			if sh.ID == pos[1] {
				if sh.URL == "" {
					return fmt.Errorf("the link %s can't be shown again (made by someone else, or by an older %s)", sh.ID, spec.Name)
				}
				fmt.Fprintln(w, sh.URL)
				return nil
			}
		}
		return fmt.Errorf("no share link %s (see %s share list)", pos[1], spec.Name)
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
		Max     int64     `json:"maxBytes"`
		Files   int       `json:"maxFiles"`
	}
	if (*maxMB != 0 || *maxFiles != 0) && !*upload {
		return usageError{errors.New("-max-mb and -max-files are for -upload links")}
	}
	if *maxDownloads != 0 && *upload {
		return usageError{errors.New("-max-downloads is for links to read")}
	}
	pw := ""
	if *password {
		if pw, err = askPassword(w); err != nil {
			return err
		}
	}
	if err := callCmd(s, "shareCreate", map[string]interface{}{"path": p, "upload": *upload, "days": *days, "maxMB": *maxMB, "maxFiles": *maxFiles,
		"password": pw, "maxDownloads": *maxDownloads}, &res); err != nil {
		return err
	}
	kind := "opens"
	if *maxDownloads > 0 {
		kind = fmt.Sprintf("allows %d download%s of", *maxDownloads, plural(*maxDownloads))
	}
	if *upload {
		kind = fmt.Sprintf("takes up to %d files, %d MB in all, into", res.Files, res.Max>>20)
	}
	with := "without the token"
	if pw != "" {
		with = "with the password"
	}
	fmt.Fprintf(w, "%s\n\nthis link %s %s until %s, %s (%s share revoke %s ends it)\n",
		res.URL, kind, p, res.Expires.Local().Format("2 Jan 2006 15:04"), with, spec.Name, res.ID)
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
	req, err := http.NewRequest(http.MethodPost, s.base()+"/cmd/"+name, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t := s.token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := s.client(10 * time.Second).Do(req)
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

// askPassword reads a link's password: twice without echo at a terminal,
// else one line of standard input.
func askPassword(w io.Writer) (string, error) {
	fd := os.Stdin.Fd()
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("-password: no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(w, "password for the link: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(w)
	if err != nil {
		return "", err
	}
	fmt.Fprint(w, "again: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(w)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("the passwords differ")
	}
	return string(a), nil
}
