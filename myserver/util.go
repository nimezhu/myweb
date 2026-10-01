package myserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

/*
			responseJson.success = 1;
	        responseJson.file = {
	          url: image.path,
	          name: image.name,
	          size: image.size
	        };
*/
type file struct {
	Url  string `json:"url"`
	Name string `json:"name"`
	Size int    `json:"size"`
}
type ret struct {
	Success int  `json:"success"`
	File    file `json:"file"`
}
type errRet struct {
	Success int    `json:"success"`
	Error   string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errRet{0, msg})
}

var errRootPath = errors.New("path refers to the root directory")

// safeJoin maps a URL path onto root without allowing escape via "..".
// It returns the filesystem path and the cleaned URL path.
func safeJoin(root, urlPath string) (string, string, error) {
	clean := path.Clean("/" + urlPath)
	if clean == "/" {
		return "", clean, errRootPath
	}
	return filepath.Join(root, filepath.FromSlash(clean)), clean, nil
}

// EnsureDir creates the parent directory of fileName.
func EnsureDir(fileName string) error {
	return os.MkdirAll(filepath.Dir(fileName), os.ModePerm)
}

// writeFileAtomic writes data via a temp file and rename so readers never
// see a half-written file and failed writes don't clobber the old one.
func writeFileAtomic(p string, data []byte) error {
	if err := EnsureDir(p); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
