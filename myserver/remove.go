package myserver

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
)

// handleRemove deletes /rm/<path>: files go to the trash (see versions.go),
// empty directories are removed. If-Match makes it conditional.
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Post data only", http.StatusMethodNotAllowed)
		return
	}
	p, dir, err := safeJoin(s.cfg.Root, r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if IsReserved(dir) {
		writeError(w, http.StatusForbidden, errReserved.Error())
		return
	}
	if !s.can(r, ActWrite, dir) {
		writeForbidden(w, ActWrite, dir)
		return
	}
	unlock := s.locks.lock(p)
	defer unlock()
	if msg := precondition(r, p); msg != "" {
		writePreconditionFailed(w, p, msg)
		return
	}
	fi, err := os.Lstat(p)
	if err == nil && fi.Mode().IsRegular() {
		err = s.trashFile(dir, userName(UserFrom(r.Context())))
	} else if err == nil {
		err = os.Remove(p) // empty directory or symlink
	}
	if err != nil {
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
