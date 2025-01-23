package myserver

import (
	"net/http"
	"os"
)

func CreateRemoveHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := r.URL.Path
		switch r.Method {
		case http.MethodPost:
			p := root + dir
			removeFile(p, w, r)
		case http.MethodGet:
			w.Write([]byte("Post data only"))
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}

	}
}

func removeFile(path string, w http.ResponseWriter, r *http.Request) {
	err := os.Remove(path)
	if err != nil {
		w.Write([]byte("Error remove " + path))
	}
}
