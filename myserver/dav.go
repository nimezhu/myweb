package myserver

// WebDAV: the data folder as a network drive, at /dav/, for Finder, Windows
// Explorer, Linux file managers, phone apps and tools like rclone.
//
// It follows the same rules as the rest of the API: .myweb is hidden; a
// file lands atomically and the one it replaces is kept as a version;
// deleting moves files to the trash (a folder's files one by one); read-only
// mode refuses changes; multi-user, people's permissions apply. Apps' pages
// and share links can't use it.
//
// File managers can't send "Authorization: Bearer", so here the token is
// also accepted as the password of HTTP Basic auth (any user name). Only
// here: browsers send saved Basic passwords along with other sites' requests.

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
	"golang.org/x/net/webdav"
)

// DAVPath is where the data folder is served over WebDAV.
const DAVPath = "/dav/"

// DAVURL is the WebDAV address ("" when it is off).
func (s *Server) DAVURL() string {
	if s.cfg.NoDAV {
		return ""
	}
	return s.URL() + DAVPath
}

func isDAVPath(p string) bool { return p == "/dav" || strings.HasPrefix(p, DAVPath) }

func (s *Server) davHandler() http.Handler {
	h := &webdav.Handler{
		Prefix:     strings.TrimSuffix(DAVPath, "/"),
		FileSystem: davFS{s},
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
				log.Printf("webdav %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()).IsApp() { // a page in a browser, not a drive
			writeError(w, http.StatusForbidden, "pages can't use WebDAV")
			return
		}
		reads := false
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			reads = true
		}
		if (!reads || s.cfg.Global || s.cfg.Access != nil) && !s.Authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="myweb", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, "the password is the token")
			return
		}
		if !reads && s.ReadOnly() {
			writeError(w, http.StatusForbidden, "server is in read-only mode")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// davFS is the data folder as webdav sees it.
type davFS struct{ s *Server }

// check maps name (slash-separated, from the handler) to its URL and file
// paths, if the request's user may do act there.
func (d davFS) check(ctx context.Context, name string, act Action) (string, string, error) {
	p := path.Clean("/" + name)
	if IsReserved(p) {
		return "", "", os.ErrNotExist
	}
	u := UserFrom(ctx)
	fp := d.s.ResolvePath(p)
	switch act {
	case ActRead:
		fi, err := os.Stat(fp)
		if !d.s.visible(u, p, err == nil && fi.IsDir()) {
			return "", "", os.ErrNotExist
		}
	default:
		if p == "/" || !d.s.allowed(u, act, p) {
			return "", "", os.ErrPermission
		}
	}
	return p, fp, nil
}

func (d davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	_, fp, err := d.check(ctx, name, ActWrite)
	if err != nil {
		return err
	}
	return os.Mkdir(fp, 0o755)
}

func (d davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) == 0 {
		p, fp, err := d.check(ctx, name, ActRead)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(fp)
		if err != nil {
			return nil, err
		}
		return &davFile{File: f, fs: d, ctx: ctx, p: p}, nil
	}
	p, fp, err := d.check(ctx, name, ActWrite)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(fp); err == nil && fi.IsDir() {
		return nil, os.ErrInvalid
	}
	if flag&os.O_TRUNC == 0 { // not a whole new file (the handler doesn't do this)
		if _, err := os.Stat(fp); err == nil {
			return nil, os.ErrPermission
		}
	}
	// a new version lands whole when it is closed
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(fp), "."+filepath.Base(fp)+".dav*")
	if err != nil {
		return nil, err
	}
	return &davWriter{File: tmp, s: d.s, p: p, dst: fp}, nil
}

// RemoveAll moves name's files to the trash, then removes the folders they
// leave empty. A file that can't be moved stays, and so does its folder.
func (d davFS) RemoveAll(ctx context.Context, name string) error {
	p, fp, err := d.check(ctx, name, ActWrite)
	if err != nil {
		return err
	}
	by := userName(UserFrom(ctx))
	fi, err := os.Lstat(fp)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return d.remove(p, fp, fi, by)
	}
	var dirs []string
	var firstErr error
	filepath.WalkDir(fp, func(f string, e fs.DirEntry, err error) error {
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			return nil
		}
		if e.IsDir() {
			dirs = append(dirs, f)
			return nil
		}
		rel, _ := filepath.Rel(d.s.cfg.Root, f)
		info, err := e.Info()
		if err == nil {
			err = d.remove("/"+filepath.ToSlash(rel), f, info, by)
		}
		if err != nil {
			firstErr = errors.Join(firstErr, err)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first; never with contents
		os.Remove(dirs[i])
	}
	return firstErr
}

func (d davFS) remove(p, fp string, fi os.FileInfo, by string) error {
	unlock := d.s.locks.lock(fp)
	defer unlock()
	if fi.Mode().IsRegular() {
		return d.s.trashFile(p, by)
	}
	return os.Remove(fp) // a symbolic link, not what it leads to
}

func (d davFS) Rename(ctx context.Context, oldName, newName string) error {
	_, from, err := d.check(ctx, oldName, ActWrite)
	if err != nil {
		return err
	}
	_, to, err := d.check(ctx, newName, ActWrite)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist // the handler has trashed it first when it may be replaced
	}
	return fsx.Rename(from, to)
}

func (d davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	_, fp, err := d.check(ctx, name, ActRead)
	if err != nil {
		return nil, err
	}
	return os.Stat(fp)
}

// davFile is a file or folder opened to read; a folder lists only what the
// user may see, and never .myweb.
type davFile struct {
	*os.File
	fs  davFS
	ctx context.Context
	p   string
}

func (f *davFile) Write([]byte) (int, error) { return 0, os.ErrPermission }

func (f *davFile) Readdir(n int) ([]os.FileInfo, error) {
	list, err := f.File.Readdir(n)
	out := list[:0]
	u := UserFrom(f.ctx)
	for _, fi := range list {
		p := path.Join(f.p, fi.Name())
		if !IsReserved(p) && f.fs.s.visible(u, p, fi.IsDir()) {
			out = append(out, fi)
		}
	}
	return out, err
}

// davWriter is a new version of a file, written to a temporary file beside
// it; Close puts it in place, keeping the old one as a version.
type davWriter struct {
	*os.File
	s      *Server
	p, dst string
	failed bool
}

func (w *davWriter) Write(b []byte) (int, error) {
	n, err := w.File.Write(b)
	if err != nil {
		w.failed = true
	}
	return n, err
}

// ReadFrom is what io.Copy uses (the handler's PUT): a body cut short must
// not be put in place.
func (w *davWriter) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.Copy(struct{ io.Writer }{w.File}, r)
	if err != nil {
		w.failed = true
	}
	return n, err
}

func (w *davWriter) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }

func (w *davWriter) Close() error {
	tmp := w.File.Name()
	if err := w.File.Close(); err != nil || w.failed {
		os.Remove(tmp)
		if err == nil {
			err = errors.New("write failed")
		}
		return err
	}
	os.Chmod(tmp, 0o644)
	unlock := w.s.locks.lock(w.dst)
	defer unlock()
	kept := w.s.KeepVersion(w.p)
	if err := fsx.Rename(tmp, w.dst); err != nil {
		os.Remove(tmp)
		if kept != "" {
			os.Remove(kept)
		}
		return err
	}
	return nil
}
