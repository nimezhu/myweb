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

func CreateLsHandleFunc(root string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := filepath.Join(root, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
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
