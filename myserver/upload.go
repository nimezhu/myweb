package myserver

// Uploads are streamed to disk (never held in memory) and land atomically:
// each file is written to a temp file next to its destination, then renamed.
//
//	POST /cmd/uploadFile          one file (field "myFile") into uploads/<md5>_<name>;
//	                              kept for existing clients such as the editor.js image tool
//	POST /up/<folder>?onconflict= any number of files into <folder>; a file name may contain
//	                              sub-folders ("photos/2024/a.jpg"), e.g. from a folder upload.
//	                              onconflict: rename (default, "a (1).txt"), overwrite (the
//	                              replaced file is kept as a version), or fail (409)

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	defaultMaxUpload = 100 << 20
	maxUploadFiles   = 1000
)

func (s *Server) maxUpload() int64 {
	if s.cfg.MaxUpload > 0 {
		return s.cfg.MaxUpload
	}
	return defaultMaxUpload
}

var errTooLarge = errors.New("file too large")

// receive streams src into a temp file in dir, at most max bytes. On success
// the caller owns the temp file and must rename or remove it.
func receive(dir string, src io.Reader, max int64, h io.Writer) (tmp string, size int64, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(dir, ".upload.tmp*")
	if err != nil {
		return "", 0, err
	}
	var w io.Writer = f
	if h != nil {
		w = io.MultiWriter(f, h)
	}
	size, err = io.Copy(w, io.LimitReader(src, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && size > max {
		err = errTooLarge
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), size, nil
}

func (s *Server) uploadTooLarge(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file too large (max %d MiB)", s.maxUpload()>>20))
}

// handleUploadFile is the original single-file, content-addressed upload.
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "post only")
		return
	}
	max := s.maxUpload()
	if r.ContentLength > max+1<<20 { // allow for multipart headers
		s.uploadTooLarge(w)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			writeError(w, http.StatusBadRequest, "missing form file 'myFile'")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
			return
		}
		if part.FormName() != "myFile" || part.FileName() == "" {
			part.Close()
			continue
		}
		dirFS := s.ResolvePath("/uploads")
		h := md5.New()
		tmp, size, err := receive(dirFS, part, max, h)
		part.Close()
		if errors.Is(err, errTooLarge) {
			s.uploadTooLarge(w)
			return
		}
		if err != nil {
			log.Printf("upload: %v", err)
			writeError(w, http.StatusInternalServerError, "create file fail")
			return
		}
		id := fmt.Sprintf("%x_%s", h.Sum(nil), sanitizeFilename(part.FileName()))
		dst := filepath.Join(dirFS, id)
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			writeError(w, http.StatusInternalServerError, "create file fail")
			return
		}
		// TODO Upload Using Cookie and Prefix ...
		f := file{"/f/uploads/" + id, id, int(size), ""}
		if fi, err := os.Stat(dst); err == nil {
			f.ETag = fileETag(fi)
		}
		writeJSON(w, http.StatusOK, ret{1, f})
		log.Printf("uploaded /uploads/%s (%d bytes)", id, size)
		return
	}
}

type uploaded struct {
	Name    string `json:"name"` // as sent
	Path    string `json:"path"` // where it landed
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	ETag    string `json:"etag,omitempty"`
	Renamed bool   `json:"renamed,omitempty"`
}

// handleUp stores every file part of a multipart request under /up/<folder>.
func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "post only")
		return
	}
	folder := path.Clean("/" + r.URL.Path)
	if IsReserved(folder) {
		writeError(w, http.StatusForbidden, errReserved.Error())
		return
	}
	mode := r.URL.Query().Get("onconflict")
	switch mode {
	case "":
		mode = "rename"
	case "rename", "overwrite", "fail":
	default:
		writeError(w, http.StatusBadRequest, "onconflict must be rename, overwrite or fail")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return
	}
	var done []uploaded
	fail := func(status int, msg string) {
		writeJSON(w, status, map[string]interface{}{"success": 0, "error": msg, "files": done})
	}
	for n := 0; ; n++ {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "invalid upload: "+err.Error())
			return
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		if n >= maxUploadFiles {
			part.Close()
			fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d files per request", maxUploadFiles))
			return
		}
		res, status, err := s.storePart(folder, part, mode)
		part.Close()
		if err != nil {
			if errors.Is(err, errTooLarge) {
				w.Header().Set("Connection", "close")
			}
			fail(status, err.Error())
			return
		}
		done = append(done, res)
	}
	if len(done) == 0 {
		fail(http.StatusBadRequest, "no files in the request")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": 1, "files": done})
}

func (s *Server) storePart(folder string, part *multipart.Part, mode string) (uploaded, int, error) {
	// Browsers send only the base name, unless the page passes a relative
	// path (folder uploads); clean every segment of it.
	name := rawFileName(part)
	var segs []string
	for _, seg := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			continue // no climbing out of the folder
		}
		segs = append(segs, sanitizeFilename(seg))
	}
	rel := path.Join(segs...)
	if rel == "" {
		return uploaded{}, http.StatusBadRequest, errors.New("invalid file name")
	}
	urlPath := path.Join(folder, rel)
	if IsReserved(urlPath) {
		return uploaded{}, http.StatusForbidden, errReserved
	}
	dst := s.ResolvePath(urlPath)
	tmp, size, err := receive(filepath.Dir(dst), part, s.maxUpload(), nil)
	if errors.Is(err, errTooLarge) {
		return uploaded{}, http.StatusRequestEntityTooLarge, fmt.Errorf("%s: file too large (max %d MiB)", rel, s.maxUpload()>>20)
	}
	if err != nil {
		log.Printf("upload %s: %v", urlPath, err)
		return uploaded{}, http.StatusInternalServerError, fmt.Errorf("%s: cannot store the file", rel)
	}
	defer os.Remove(tmp) // no-op after a successful rename

	res := uploaded{Name: name, Size: size}
	unlock := s.locks.lock(dst)
	defer unlock()
	if fi, err := os.Lstat(dst); err == nil {
		switch {
		case fi.IsDir():
			return res, http.StatusConflict, fmt.Errorf("%s: a folder has that name", rel)
		case mode == "fail":
			return res, http.StatusConflict, fmt.Errorf("%s: already exists", rel)
		case mode == "rename":
			urlPath, dst = s.freeName(urlPath)
			res.Renamed = true
		}
	}
	kept := s.KeepVersion(urlPath) // overwrite: the replaced file stays in its history
	if err := os.Rename(tmp, dst); err != nil {
		if kept != "" {
			os.Remove(kept)
		}
		return res, http.StatusInternalServerError, fmt.Errorf("%s: cannot store the file", rel)
	}
	res.Path, res.URL = urlPath, "/f"+urlPath
	if fi, err := os.Stat(dst); err == nil {
		res.ETag = fileETag(fi)
	}
	log.Printf("uploaded %s (%d bytes)", urlPath, size)
	return res, 0, nil
}

// freeName finds "name (1).ext", "name (2).ext", … that doesn't exist yet.
func (s *Server) freeName(urlPath string) (string, string) {
	dir, base := path.Split(urlPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		p := dir + fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if fsPath := s.ResolvePath(p); !fileExists(fsPath) {
			return p, fsPath
		}
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

// rawFileName is the part's file name as sent, sub-folders included
// (Part.FileName keeps only the last element).
func rawFileName(part *multipart.Part) string {
	if _, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	return part.FileName()
}
