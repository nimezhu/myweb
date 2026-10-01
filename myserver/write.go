package myserver

import (
	"errors"
	"io"
	"log"
	"net/http"
	"path"
)

// MaxWriteSize limits the body accepted by /w/.
var MaxWriteSize int64 = 256 << 20

func CreateWriteHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Post data only", http.StatusMethodNotAllowed)
			return
		}
		p, dir, err := safeJoin(root, r.URL.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
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
		if err := writeFileAtomic(p, reqBody); err != nil {
			log.Printf("write %s: %v", p, err)
			writeError(w, http.StatusInternalServerError, "write failed")
			return
		}
		// TODO with url prefix problem
		writeJSON(w, http.StatusOK, ret{1, file{"/f" + dir, path.Base(dir), len(reqBody)}})
		log.Printf("wrote %s (%d bytes)", dir, len(reqBody))
	}
}
