package myserver

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"
)

type FileInfo struct {
	Name      string      `json:"name"`
	Size      int64       `json:"size"`
	Mode      os.FileMode `json:"mode"`
	ModTime   time.Time   `json:"modtime"`
	IsDir     bool        `json:"isdir"`
	IsSymlink bool        `json:"issymlink"`
}

// CreateLsHandleFunc lists folders under root (single-user, no permission checks).
func CreateLsHandleFunc(root string) func(http.ResponseWriter, *http.Request) {
	return listFunc(root, nil)
}

// handleLs lists /ls/<dir>, showing only what the user may see.
func (s *Server) handleLs(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	clean := path.Clean("/" + r.URL.Path)
	if !s.visible(u, clean, true) {
		writeError(w, http.StatusNotFound, "cannot list "+r.URL.Path)
		return
	}
	listFunc(s.cfg.Root, func(p string, isDir bool) bool { return s.visible(u, p, isDir) })(w, r)
}

func listFunc(root string, show func(p string, isDir bool) bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		dir := filepath.Join(root, filepath.FromSlash(clean))
		entries, err := os.ReadDir(dir)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeError(w, status, "cannot list "+r.URL.Path)
			return
		}
		fn := make([]FileInfo, 0, len(entries))
		for _, entry := range entries {
			if clean == "/" && entry.Name() == metaDir {
				continue // the server's own history and trash
			}
			if show != nil && !show(path.Join(clean, entry.Name()), entry.IsDir()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue // removed between ReadDir and Info
			}
			fn = append(fn, FileInfo{
				Name:      entry.Name(),
				Size:      info.Size(),
				Mode:      info.Mode(),
				ModTime:   info.ModTime(),
				IsDir:     entry.IsDir(),
				IsSymlink: info.Mode()&os.ModeSymlink != 0,
			})
		}
		writeJSON(w, http.StatusOK, fn)
	}
}
