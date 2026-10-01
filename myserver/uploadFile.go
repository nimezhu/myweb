package myserver

import (
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

// MaxUploadSize limits files accepted by /cmd/uploadFile.
var MaxUploadSize int64 = 10 << 20

func CreateUploadFileHandleFunc(root string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "post only")
			return
		}
		tooLarge := fmt.Sprintf("File Too Large (max %dMb)", MaxUploadSize>>20)
		if r.ContentLength > MaxUploadSize {
			w.Header().Set("Connection", "close")
			writeError(w, http.StatusExpectationFailed, tooLarge)
			return
		}
		// allow some slack for multipart headers
		r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize+1<<20)
		if err := r.ParseMultipartForm(MaxUploadSize); err != nil {
			writeError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
			return
		}
		defer r.MultipartForm.RemoveAll()

		f, handler, err := r.FormFile("myFile")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing form file 'myFile'")
			return
		}
		defer f.Close()
		if handler.Size > MaxUploadSize {
			writeError(w, http.StatusExpectationFailed, tooLarge)
			return
		}
		fileBytes, err := io.ReadAll(f)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read upload: "+err.Error())
			return
		}

		name := sanitizeFilename(handler.Filename)
		id := fmt.Sprintf("%x_%s", md5.Sum(fileBytes), name)
		dir := "/uploads/" + id // relative url path
		p := filepath.Join(root, "uploads", id)
		if err := writeFileAtomic(p, fileBytes); err != nil {
			log.Printf("upload %s: %v", p, err)
			writeError(w, http.StatusInternalServerError, "create file fail")
			return
		}
		// TODO Upload Using Cookie and Prefix ...
		writeJSON(w, http.StatusOK, ret{1, file{"/f" + dir, id, len(fileBytes)}})
		log.Printf("uploaded %s (%d bytes)", dir, len(fileBytes))
	}
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	return name
}
