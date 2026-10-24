package myserver

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

// handleRemove deletes /rm/<path>: files go to the trash (see versions.go),
// empty directories are removed. If-Match makes it conditional.
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Post data only", http.StatusMethodNotAllowed)
		return
	}
	p, dir, err := safeJoin(s.cfg.Root, r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if IsReserved(dir) {
		writeError(w, http.StatusForbidden, errReserved.Error())
		return
	}
	if !s.can(r, ActWrite, dir) {
		writeForbidden(w, ActWrite, dir)
		return
	}
	unlock := s.locks.lock(p)
	defer unlock()
	if msg := precondition(r, p); msg != "" {
		writePreconditionFailed(w, p, msg)
		return
	}
	fi, err := os.Lstat(p)
	if err == nil && fi.Mode().IsRegular() {
		err = s.trashFile(dir, userName(UserFrom(r.Context())))
	} else if err == nil {
		err = os.Remove(p) // empty directory or symlink
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, "remove failed: "+r.URL.Path)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"success": 1})
	log.Printf("removed %s", r.URL.Path)
}

// removeTree moves p's files to the trash (a folder's one by one), then
// removes the folders they leave empty, and returns how many files went. A
// file that can't be moved stays, and so does its folder. by is who.
func (s *Server) removeTree(p, by string) (int, error) {
	fp := s.ResolvePath(p)
	fi, err := os.Lstat(fp)
	if err != nil {
		return 0, err
	}
	if !fi.IsDir() {
		return 1, s.removeOne(p, fp, fi, by)
	}
	var dirs []string
	var errs error
	n := 0
	filepath.WalkDir(fp, func(f string, e fs.DirEntry, err error) error {
		if err != nil {
			errs = errors.Join(errs, err)
			return nil
		}
		if e.IsDir() {
			dirs = append(dirs, f)
			return nil
		}
		rel, _ := filepath.Rel(s.cfg.Root, f)
		info, err := e.Info()
		if err == nil {
			err = s.removeOne("/"+filepath.ToSlash(rel), f, info, by)
		}
		if err != nil {
			errs = errors.Join(errs, err)
		} else {
			n++
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first; never with contents
		os.Remove(dirs[i])
	}
	return n, errs
}

func (s *Server) removeOne(p, fp string, fi os.FileInfo, by string) error {
	unlock := s.locks.lock(fp)
	defer unlock()
	if fi.Mode().IsRegular() {
		return s.trashFile(p, by)
	}
	return os.Remove(fp) // a symbolic link, not what it leads to
}

// registerRemoveCmd: "rm" deletes files and folders, everything in them to
// the trash.
func (s *Server) registerRemoveCmd() {
	s.RegisterCmd("rm", func(_ context.Context, req *CmdRequest) (interface{}, error) {
		var p struct {
			Paths []string `json:"paths"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if len(p.Paths) == 0 {
			return nil, Errorf(http.StatusBadRequest, "paths is required")
		}
		for i, x := range p.Paths { // all allowed, or none removed
			x = path.Clean("/" + x)
			switch {
			case x == "/":
				return nil, Errorf(http.StatusBadRequest, "the whole data folder can't be removed")
			case IsReserved(x):
				return nil, Errorf(http.StatusForbidden, "%s is managed by the server", x)
			}
			fp, err := req.Path(ActWrite, x)
			if err != nil {
				return nil, err
			}
			if _, err := os.Lstat(fp); err != nil {
				return nil, Errorf(http.StatusNotFound, "not found: %s", x)
			}
			p.Paths[i] = x
		}
		files := 0
		failed := []string{}
		for _, x := range p.Paths {
			n, err := s.removeTree(x, userName(req.User))
			files += n
			if err != nil {
				failed = append(failed, x)
				log.Printf("rm %s: %v", x, err)
			} else {
				log.Printf("removed %s", x)
			}
		}
		if len(failed) == len(p.Paths) && files == 0 {
			return nil, Errorf(http.StatusConflict, "could not remove %s", failed[0])
		}
		return map[string]interface{}{"files": files, "failed": failed}, nil
	}, Writes(), Describe(`delete files and folders (everything in them goes to the trash): {"paths":["/old","/a.txt"]}`))
}
