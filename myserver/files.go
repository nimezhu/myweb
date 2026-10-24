package myserver

import (
	"io/fs"
	"net/http"
	"os"
	"path"
)

// guardFiles checks /f/ permissions before anything else runs, so not even
// an ETag is sent for a file the user may not read. Without permission the
// answer is 404: it doesn't reveal whether the file exists.
func (s *Server) guardFiles(next http.Handler) http.Handler {
	if s.cfg.Access == nil && s.cfg.App == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Access == nil && !UserFrom(r.Context()).IsApp() {
			next.ServeHTTP(w, r)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		fi, err := os.Stat(s.ResolvePath(p))
		if !s.visible(UserFrom(r.Context()), p, err == nil && fi.IsDir()) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveFiles is the /f/ file server; multi-user, it serves through a view of
// the folder that hides what the user may not see.
func (s *Server) serveFiles(w http.ResponseWriter, r *http.Request) {
	var fsys http.FileSystem = http.Dir(s.cfg.Root)
	if s.cfg.Access != nil || UserFrom(r.Context()).IsApp() {
		fsys = userFS{fsys, s, UserFrom(r.Context())}
	}
	http.FileServer(fsys).ServeHTTP(w, r)
}

// userFS hides files the user may not read and folders they may not see,
// including in folder listings and a folder's index.html.
type userFS struct {
	http.FileSystem
	s *Server
	u *User
}

func (f userFS) Open(name string) (http.File, error) {
	file, err := f.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !f.s.visible(f.u, name, fi.IsDir()) {
		file.Close()
		return nil, fs.ErrNotExist
	}
	if fi.IsDir() {
		return userDir{file, f, path.Clean("/" + name)}, nil
	}
	return file, nil
}

type userDir struct {
	http.File
	f   userFS
	dir string
}

func (d userDir) Readdir(n int) ([]os.FileInfo, error) {
	all, err := d.File.Readdir(n)
	shown := all[:0]
	for _, fi := range all {
		if d.dir == "/" && fi.Name() == metaDir {
			continue
		}
		if d.f.s.visible(d.f.u, path.Join(d.dir, fi.Name()), fi.IsDir()) {
			shown = append(shown, fi)
		}
	}
	return shown, err
}
