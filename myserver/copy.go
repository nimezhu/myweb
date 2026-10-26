package myserver

// "cp" copies files and folders. A copy keeps the original's modification
// time; symbolic links are left out (as /zip/ does).

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nimezhu/myweb/internal/fsx"
)

// MaxCopyBytes limits what one cp copies.
var MaxCopyBytes int64 = 20 << 30

type cpParams struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"` // a file there is replaced (and kept as a version)
	Rename    bool   `json:"rename"`    // a name taken gives "name (1).ext"
}

type cpResult struct {
	From  string `json:"from"`
	To    string `json:"to"` // where it went (renamed, maybe)
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func (s *Server) registerCopyCmd() {
	s.RegisterCmd("cp", s.cmdCp, Writes(),
		Describe(`copy a file or folder: {"from":"/a.txt","to":"/b/a.txt","overwrite":false,"rename":false}; rename picks a free name like "a (1).txt"`))
}

func (s *Server) cmdCp(ctx context.Context, req *CmdRequest) (interface{}, error) {
	var p cpParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	from, to := path.Clean("/"+p.From), path.Clean("/"+p.To)
	switch {
	case from == "/" || to == "/":
		return nil, Errorf(http.StatusBadRequest, "from and to must not be the root")
	case IsReserved(from) || IsReserved(to):
		return nil, Errorf(http.StatusForbidden, "%s is managed by the server", metaDir)
	case strings.HasPrefix(to, from+"/"):
		return nil, Errorf(http.StatusBadRequest, "cannot copy a folder into itself")
	}
	src, err := req.Path(ActRead, from)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(src)
	if err != nil || !(fi.IsDir() || fi.Mode().IsRegular()) {
		return nil, Errorf(http.StatusNotFound, "not found: %s", from)
	}
	if from == to && !p.Rename {
		return nil, Errorf(http.StatusBadRequest, "from and to are the same (set rename for a copy beside it)")
	}
	if _, err := req.Path(ActWrite, to); err != nil {
		return nil, err
	}
	if fileExists(s.ResolvePath(to)) {
		switch {
		case p.Rename:
			to, _ = s.freeName(to)
		case !p.Overwrite:
			return nil, Errorf(http.StatusConflict, "already exists: %s (set overwrite to replace it, or rename)", to)
		case fi.IsDir():
			return nil, Errorf(http.StatusConflict, "will not overwrite folder %s", to)
		}
		if _, err := req.Path(ActWrite, to); err != nil {
			return nil, err
		}
	}
	if dfi, err := os.Stat(s.ResolvePath(to)); err == nil && dfi.IsDir() {
		return nil, Errorf(http.StatusConflict, "will not overwrite folder %s", to)
	}
	res := cpResult{From: from, To: to}
	if !fi.IsDir() {
		if err := s.copyOne(src, to, fi); err != nil {
			return nil, Errorf(http.StatusConflict, "copy failed: %v", err)
		}
		res.Files, res.Bytes = 1, fi.Size()
		req.Record(Activity{Op: "copy", Path: from, To: to, Files: 1, Size: fi.Size()})
		return res, nil
	}

	// a folder: what the user may read of it, below a new folder at to
	var total int64
	err = filepath.WalkDir(src, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip what can't be read
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(src, fp)
		srcURL := path.Join(from, filepath.ToSlash(rel))
		dstURL := path.Join(to, filepath.ToSlash(rel))
		if d.IsDir() {
			if fp != src && !req.Visible(srcURL, true) {
				return filepath.SkipDir
			}
			if !req.Can(ActWrite, dstURL) {
				return Errorf(http.StatusForbidden, "no write permission for %s", dstURL)
			}
			return os.MkdirAll(s.ResolvePath(dstURL), 0o755)
		}
		if !d.Type().IsRegular() || !req.Can(ActRead, srcURL) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if total += fi.Size(); total > MaxCopyBytes {
			return Errorf(http.StatusRequestEntityTooLarge, "more than %d GiB to copy; copied %d files", MaxCopyBytes>>30, res.Files)
		}
		if err := s.copyOne(fp, dstURL, fi); err != nil {
			return Errorf(http.StatusConflict, "%s: %v (copied %d files)", srcURL, err, res.Files)
		}
		res.Files++
		res.Bytes += fi.Size()
		return nil
	})
	if res.Files > 0 || err == nil {
		req.Record(Activity{Op: "copy", Path: from, To: to, Files: res.Files, Size: res.Bytes})
	}
	if err != nil {
		var ce *CmdError
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, Errorf(http.StatusConflict, "copy failed: %v (copied %d files)", err, res.Files)
	}
	log.Printf("copied %s to %s (%d files)", from, to, res.Files)
	return res, nil
}

// copyOne copies the file src (fi) to the URL path to: whole into a temp
// file beside it, then in place. A file it replaces is kept as a version.
func (s *Server) copyOne(src, to string, fi os.FileInfo) error {
	dst := s.ResolvePath(to)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once in place
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	os.Chmod(tmp.Name(), 0o644)
	os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime())
	defer s.locks.lock(dst)()
	kept := s.KeepVersion(to)
	if err := fsx.Rename(tmp.Name(), dst); err != nil {
		if kept != "" {
			os.Remove(kept)
		}
		return err
	}
	return nil
}
