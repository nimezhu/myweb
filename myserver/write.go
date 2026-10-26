package myserver

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
)

// MaxWriteSize limits the body accepted by /w/.
var MaxWriteSize int64 = 256 << 20

// handleWrite stores the request body at /w/<path>. The previous content is
// kept as a version; If-Match / If-None-Match make the write conditional.
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
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
	reqBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWriteSize))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	unlock := s.locks.lock(p)
	defer unlock()
	if msg := precondition(r, p); msg != "" {
		writePreconditionFailed(w, p, msg)
		return
	}
	kept := s.KeepVersion(dir)
	if err := writeFileAtomic(p, reqBody); err != nil {
		if kept != "" {
			os.Remove(kept) // still the same file as p: don't keep it as a version
		}
		log.Printf("write %s: %v", p, err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	s.recordFor(r, Activity{Op: "save", Path: dir, Size: int64(len(reqBody))})
	f := file{"/f" + dir, path.Base(dir), len(reqBody), ""}
	if fi, err := os.Stat(p); err == nil {
		f.ETag = fileETag(fi)
		w.Header().Set("ETag", f.ETag)
	}
	// TODO with url prefix problem
	writeJSON(w, http.StatusOK, ret{1, f})
	log.Printf("wrote %s (%d bytes)", dir, len(reqBody))
}
