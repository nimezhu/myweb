package myserver

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"time"
)

func ls(dir string) []os.FileInfo {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return []os.FileInfo{}
	}
	return files
}

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
		dir := r.URL.Path
		fs := ls(path.Join(root, dir))
		fn := []FileInfo{}
		for _, entry := range fs {
			issymlink := entry.Mode()&os.ModeSymlink == os.ModeSymlink
			f := FileInfo{
				Name:      entry.Name(),
				Size:      entry.Size(),
				Mode:      entry.Mode(),
				ModTime:   entry.ModTime(),
				IsDir:     entry.IsDir(),
				IsSymlink: issymlink,
			}
			fn = append(fn, f)
		}
		j, err := json.Marshal(fn)
		if err == nil {
			w.Write(j)
		} else {
			w.Write([]byte("error"))
		}

	}
}
