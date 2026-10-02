package myserver

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
)

// revalidate makes browsers check with the server before reusing a cached
// data file, and gives files an ETag so an unchanged one costs a 304.
//
// Without Cache-Control, browsers cache by heuristic (a fraction of the time
// since Last-Modified) and may serve a stale file after it changed on disk.
func revalidate(root string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Clean("/"+r.URL.Path)))); err == nil && fi.Mode().IsRegular() {
			// size + mtime, like nginx; http.ServeContent then answers
			// If-None-Match (304) and If-Range for us
			w.Header().Set("ETag", fileETag(fi))
		}
		h.ServeHTTP(w, r)
	})
}
