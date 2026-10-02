// Package plugins holds ready-made plugin commands for a myserver.Server.
package plugins

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

// RegisterFileOps adds the stat, mkdir, mv and find commands, served at
// POST /cmd/<name> with a JSON body. They also serve as examples for writing
// your own with srv.RegisterCmd; see myserver/cmd.go for the API.
func RegisterFileOps(srv *myserver.Server) {
	srv.RegisterCmd("stat", cmdStat,
		myserver.Describe(`file info: {"path":"/a.txt"}`))
	srv.RegisterCmd("mkdir", cmdMkdir, myserver.Writes(),
		myserver.Describe(`create a directory (and parents): {"path":"/a/b"}`))
	srv.RegisterCmd("mv", cmdMv, myserver.Writes(),
		myserver.Describe(`move or rename a file or directory: {"from":"/a.txt","to":"/b/a.txt","overwrite":false}`))
	srv.RegisterCmd("find", cmdFind,
		myserver.Describe(`find files by name: {"dir":"/","pattern":"*.csv","limit":200}`))
}

type pathParams struct {
	Path string `json:"path"`
}

type statResult struct {
	Path    string    `json:"path"`
	Exists  bool      `json:"exists"`
	IsDir   bool      `json:"isdir,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modtime,omitempty"`
}

func cmdStat(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	var p pathParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	fi, err := os.Stat(req.Server.ResolvePath(p.Path))
	if errors.Is(err, fs.ErrNotExist) {
		return statResult{Path: p.Path}, nil
	}
	if err != nil {
		return nil, err
	}
	return statResult{p.Path, true, fi.IsDir(), fi.Size(), fi.ModTime()}, nil
}

func cmdMkdir(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	var p pathParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if strings.Trim(p.Path, "/") == "" {
		return nil, myserver.Errorf(http.StatusBadRequest, "path is required")
	}
	if err := os.MkdirAll(req.Server.ResolvePath(p.Path), 0o755); err != nil {
		return nil, myserver.Errorf(http.StatusConflict, "mkdir failed: %v", err)
	}
	return map[string]string{"path": path.Clean("/" + p.Path)}, nil
}

type mvParams struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

func cmdMv(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	var p mvParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	from, to := path.Clean("/"+p.From), path.Clean("/"+p.To)
	switch {
	case from == "/" || to == "/":
		return nil, myserver.Errorf(http.StatusBadRequest, "from and to must not be the root")
	case from == to:
		return nil, myserver.Errorf(http.StatusBadRequest, "from and to are the same")
	case strings.HasPrefix(to, from+"/"):
		return nil, myserver.Errorf(http.StatusBadRequest, "cannot move a directory into itself")
	}
	src, dst := req.Server.ResolvePath(from), req.Server.ResolvePath(to)
	if _, err := os.Lstat(src); err != nil {
		return nil, myserver.Errorf(http.StatusNotFound, "not found: %s", from)
	}
	if fi, err := os.Lstat(dst); err == nil {
		if !p.Overwrite {
			return nil, myserver.Errorf(http.StatusConflict, "already exists: %s (set overwrite to replace it)", to)
		}
		if fi.IsDir() {
			// os.Rename only replaces empty directories; refuse rather than half-merge
			return nil, myserver.Errorf(http.StatusConflict, "will not overwrite directory %s", to)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, myserver.Errorf(http.StatusConflict, "cannot create %s: %v", path.Dir(to), err)
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, myserver.Errorf(http.StatusConflict, "move failed: %v", errors.Unwrap(err))
	}
	return map[string]string{"from": from, "to": to}, nil
}

type findParams struct {
	Dir     string `json:"dir"`
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit"`
}

type findResult struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

var errFindLimit = errors.New("limit reached")

func cmdFind(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	p := findParams{Dir: "/", Pattern: "*", Limit: 200}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if _, err := path.Match(p.Pattern, ""); err != nil {
		return nil, myserver.Errorf(http.StatusBadRequest, "bad pattern: %v", err)
	}
	if p.Limit <= 0 || p.Limit > 5000 {
		p.Limit = 5000
	}
	root := req.Server.ResolvePath("/")
	base := req.Server.ResolvePath(p.Dir)
	res := findResult{Files: []string{}}
	err := filepath.WalkDir(base, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err() // client went away
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && fp != base {
			return filepath.SkipDir
		}
		if ok, _ := path.Match(p.Pattern, d.Name()); ok && !d.IsDir() {
			if len(res.Files) >= p.Limit {
				res.Truncated = true
				return errFindLimit
			}
			rel, _ := filepath.Rel(root, fp)
			res.Files = append(res.Files, "/"+filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && err != errFindLimit {
		return nil, err
	}
	return res, nil
}
