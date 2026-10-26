package myserver

// The activity log: what was changed through the server (the file manager,
// the API, WebDAV, share links), when, and by whom, for the file manager's
// Recent view. One JSON line per change in .myweb/activity.jsonl; past
// activityMax it becomes activity.1.jsonl (and the one before goes).
// Changes made to the folder by other programs are not in it.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"sync"
	"time"
)

var activityMax int64 = 2 << 20

// Activity is one change in the log.
type Activity struct {
	Time  time.Time `json:"t"`
	Op    string    `json:"op"` // upload, save, delete, move, copy, mkdir, restore, untrash, unzip
	Path  string    `json:"path"`
	To    string    `json:"to,omitempty"`   // move, copy, unzip: where to
	Files int       `json:"n,omitempty"`    // delete, copy, unzip: how many files
	Size  int64     `json:"size,omitempty"` // upload, save
	Who   string    `json:"who,omitempty"`  // multi-user: the person
	Via   string    `json:"via,omitempty"`  // "dav", "share"
}

type activityLog struct{ mu sync.Mutex }

func (s *Server) activityFiles() (cur, old string) {
	return s.metaPath("activity.jsonl"), s.metaPath("activity.1.jsonl")
}

// record adds a to the log. A failure is logged, never the caller's.
func (s *Server) record(a Activity) {
	if junk(a.Path) && a.Op != "move" {
		return // what Finder and Explorer leave about: .DS_Store, ._name, …
	}
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	a.Time = a.Time.UTC().Truncate(time.Second)
	b, err := json.Marshal(a)
	if err != nil {
		return
	}
	cur, old := s.activityFiles()
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	if fi, err := os.Stat(cur); err == nil && fi.Size() > activityMax {
		os.Rename(cur, old)
	}
	if err := os.MkdirAll(s.metaPath(), 0o755); err != nil {
		logf("activity: %v", err)
		return
	}
	f, err := os.OpenFile(cur, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		logf("activity: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		logf("activity: %v", err)
	}
}

// recordFor adds a change made for request r (its user, if any).
func (s *Server) recordFor(r *http.Request, a Activity) {
	a.Who = userName(UserFrom(r.Context()))
	s.record(a)
}

// Record adds a change this command made to the activity log, as made by
// the user asking.
func (r *CmdRequest) Record(a Activity) {
	a.Who = userName(r.User)
	r.Server.record(a)
}

// readActivity returns the log, oldest first.
func (s *Server) readActivity() []Activity {
	cur, old := s.activityFiles()
	var all []Activity
	for _, file := range []string{old, cur} {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var a Activity
			if json.Unmarshal(sc.Bytes(), &a) == nil && a.Op != "" {
				all = append(all, a)
			}
		}
		f.Close()
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	return all
}

// activityGroup is one line of the Recent view: uploads, saves or deletes
// by one person, close in time, of one file or in one folder.
type activityGroup struct {
	Activity
	Count int       `json:"count"`           // changes in the group
	Names []string  `json:"names,omitempty"` // a few of the files, for uploads and deletes in a folder
	First time.Time `json:"first"`           // the group's oldest change; Time is its newest
}

const activityGap = 10 * time.Minute

// groupActivity groups a (oldest first) into lines, newest first.
func groupActivity(a []Activity) []activityGroup {
	var out []activityGroup
	open := map[string]int{} // group key → index in out
	for _, x := range a {
		key := x.Op + "\x00" + x.Who + "\x00" + x.Via + "\x00"
		folder := false
		switch x.Op {
		case "upload", "save", "delete":
			key += path.Dir(x.Path)
			folder = true
		default:
			key = "" // each on its own
		}
		if i, ok := open[key]; ok && key != "" && x.Time.Sub(out[i].Time) <= activityGap {
			g := &out[i]
			g.Time = x.Time
			g.Count++
			g.Size += x.Size
			g.Files += max(x.Files, 1)
			if folder && g.Path != x.Path {
				g.Path = path.Dir(x.Path) // the group is now the folder's
				if len(g.Names) < 5 && !slices.Contains(g.Names, path.Base(x.Path)) {
					g.Names = append(g.Names, path.Base(x.Path))
				}
			}
			continue
		}
		g := activityGroup{Activity: x, Count: 1, First: x.Time}
		if folder {
			g.Files = max(x.Files, 1)
			g.Names = []string{path.Base(x.Path)}
		}
		out = append(out, g)
		if key != "" {
			open[key] = len(out) - 1
		}
	}
	slices.Reverse(out) // newest first, also within a second
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

func (s *Server) registerActivityCmd() {
	s.RegisterCmd("activity", func(_ context.Context, req *CmdRequest) (interface{}, error) {
		p := struct {
			Limit int    `json:"limit"`
			Dir   string `json:"dir"`
		}{Limit: 100, Dir: "/"}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if p.Limit <= 0 || p.Limit > 1000 {
			p.Limit = 1000
		}
		dir := path.Clean("/" + p.Dir)
		under := func(x string) bool {
			return x != "" && (dir == "/" || x == dir || len(x) > len(dir) && x[:len(dir)+1] == dir+"/")
		}
		var mine []Activity
		for _, a := range s.readActivity() {
			// only what the user may see, and in dir
			if !(under(a.Path) && req.Can(ActRead, a.Path) || under(a.To) && req.Can(ActRead, a.To)) {
				continue
			}
			if a.To != "" && !req.Can(ActRead, a.To) {
				a.To = ""
			}
			if !req.Can(ActRead, a.Path) {
				a.Path = ""
			}
			mine = append(mine, a)
		}
		g := groupActivity(mine)
		more := len(g) > p.Limit
		if more {
			g = g[:p.Limit]
		}
		if g == nil {
			g = []activityGroup{}
		}
		return map[string]interface{}{"items": g, "more": more}, nil
	}, Describe(`recent changes made through the server, newest first, grouped: {"limit":100,"dir":"/"}`))
}
