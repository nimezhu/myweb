package myserver

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
)

func CreateRemoveHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Post data only", http.StatusMethodNotAllowed)
			return
		}
		p, _, err := safeJoin(root, r.URL.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := os.Remove(p); err != nil {
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
}
