package myserver

// Zip archives, both ways:
//
//	GET  /zip/<folder>                 the folder as a .zip, streamed
//	GET  /zip/<folder>?f=a&f=b         only those entries of the folder
//	POST /cmd/unzip                    unpack a .zip in the data folder:
//	     {"path":"/a.zip","to":"/a","onconflict":"rename"}
//
// The server's own folder (.myweb) and symbolic links are never archived,
// and multi-user, only what the user may read. Unpacking writes like an
// upload: each file lands atomically, and onconflict is rename (default),
// overwrite (the replaced file is kept as a version) or fail.

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Limits for unpacking, against archives that expand without end.
var (
	MaxUnzipBytes int64 = 16 << 30
	MaxUnzipFiles       = 100000
)

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "get only")
		return
	}
	u := UserFrom(r.Context())
	folder := path.Clean("/" + r.URL.Path)
	fi, err := os.Stat(s.ResolvePath(folder))
	if err != nil || !fi.IsDir() || IsReserved(folder) || !s.visible(u, folder, true) {
		writeError(w, http.StatusNotFound, "no such folder: "+folder)
		return
	}
	// the entries to archive: the folder's, or the chosen ones
	var names []string
	if picked := r.URL.Query()["f"]; len(picked) > 0 {
		for _, n := range picked {
			if n = sanitizeFilename(n); n != "" {
				names = append(names, n)
			}
		}
	} else {
		entries, err := os.ReadDir(s.ResolvePath(folder))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read "+folder)
			return
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}

	base := path.Base(folder)
	if folder == "/" {
		base = filepath.Base(s.cfg.Root)
	}
	if len(r.URL.Query()["f"]) == 1 {
		base = strings.TrimSuffix(names[0], path.Ext(names[0]))
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+".zip"))
	if r.Method == http.MethodHead {
		return
	}
	zw := zip.NewWriter(w)
	for _, n := range names {
		if err := s.zipTree(r.Context(), zw, u, path.Join(folder, n), n); err != nil {
			// the headers are sent: all that can be done is to cut the archive short
			log.Printf("zip %s: %v", folder, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("zip %s: %v", folder, err)
	}
}

// zipTree adds the file or folder at urlPath to zw as name.
func (s *Server) zipTree(ctx context.Context, zw *zip.Writer, u *User, urlPath, name string) error {
	if IsReserved(urlPath) {
		return nil
	}
	root := s.ResolvePath(urlPath)
	return filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // gone or unreadable: leave it out
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(root, fp)
		p := path.Join(urlPath, filepath.ToSlash(rel))
		entry := path.Join(name, filepath.ToSlash(rel))
		if IsReserved(p) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if !s.visible(u, p, true) {
				return filepath.SkipDir
			}
			if s.allowed(u, ActRead, p) {
				_, err := zw.CreateHeader(&zip.FileHeader{Name: entry + "/", Modified: modTime(d)})
				return err
			}
			return nil
		}
		if !d.Type().IsRegular() || !s.allowed(u, ActRead, p) {
			return nil // links, devices, and files the user may not read
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name, h.Method = entry, zip.Deflate
		if compressed(entry) {
			h.Method = zip.Store
		}
		f, err := os.Open(fp)
		if err != nil {
			return nil
		}
		defer f.Close()
		dst, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(dst, f)
		}
		return err
	})
}

func modTime(d fs.DirEntry) time.Time {
	if info, err := d.Info(); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// compressed files gain nothing from deflate.
func compressed(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".7z", ".rar",
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".heic",
		".mp3", ".mp4", ".m4a", ".mov", ".mkv", ".webm", ".ogg", ".opus",
		".pdf", ".docx", ".xlsx", ".pptx", ".bam", ".cram", ".bw", ".bigwig":
		return true
	}
	return false
}

type unzipParams struct {
	Path       string `json:"path"`
	To         string `json:"to"`
	OnConflict string `json:"onconflict"`
}

type unzipResult struct {
	To      string `json:"to"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Renamed int    `json:"renamed,omitempty"`
}

func (s *Server) cmdUnzip(ctx context.Context, req *CmdRequest) (interface{}, error) {
	p := unzipParams{OnConflict: "rename"}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	switch p.OnConflict {
	case "rename", "overwrite", "fail":
	default:
		return nil, Errorf(http.StatusBadRequest, "onconflict must be rename, overwrite or fail")
	}
	src := path.Clean("/" + p.Path)
	fsSrc, err := req.Path(ActRead, src)
	if err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(fsSrc)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Errorf(http.StatusNotFound, "not found: %s", src)
	}
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, "%s is not a zip archive: %v", src, err)
	}
	defer zr.Close()

	to := path.Clean("/" + p.To)
	if p.To == "" { // next to the archive, in a folder named after it
		to = strings.TrimSuffix(src, path.Ext(src))
		if to == src {
			to += " (unzipped)"
		}
		if _, err := os.Lstat(s.ResolvePath(to)); err == nil {
			to, _ = s.freeName(to)
		}
	}
	if IsReserved(to) {
		return nil, Errorf(http.StatusForbidden, "%s is managed by the server", to)
	}
	if fi, err := os.Stat(s.ResolvePath(to)); err == nil && !fi.IsDir() {
		return nil, Errorf(http.StatusConflict, "%s is a file", to)
	}

	res := unzipResult{To: to}
	files := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !f.Mode().IsRegular() || junk(f.Name) {
			continue // folders are made as needed; links are not unpacked
		}
		if files++; files > MaxUnzipFiles {
			return res, Errorf(http.StatusRequestEntityTooLarge, "more than %d files; unpacked %d", MaxUnzipFiles, res.Files)
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rel := cleanRelPath(f.Name)
		if rel == "" {
			continue
		}
		dst := path.Join(to, rel)
		if IsReserved(dst) {
			continue
		}
		fsDst, err := req.Path(ActWrite, dst)
		if err != nil {
			return res, err
		}
		rc, err := f.Open()
		if err != nil {
			return res, Errorf(http.StatusBadRequest, "%s: %v", rel, err)
		}
		tmp, size, err := receive(filepath.Dir(fsDst), rc, MaxUnzipBytes-res.Bytes, nil)
		rc.Close()
		if errors.Is(err, errTooLarge) {
			return res, Errorf(http.StatusRequestEntityTooLarge, "unpacks to more than %d GiB; unpacked %d files", MaxUnzipBytes>>30, res.Files)
		}
		if err != nil {
			return res, Errorf(http.StatusBadRequest, "%s: %v", rel, err)
		}
		placed, renamed, status, err := s.place(tmp, dst, rel, p.OnConflict)
		os.Remove(tmp) // no-op after placing it
		if err != nil {
			return res, Errorf(status, "%v (unpacked %d files)", err, res.Files)
		}
		if !f.Modified.IsZero() {
			os.Chtimes(s.ResolvePath(placed), f.Modified, f.Modified)
		}
		res.Files++
		res.Bytes += size
		if renamed {
			res.Renamed++
		}
	}
	log.Printf("unzipped %s into %s (%d files, %d bytes)", src, to, res.Files, res.Bytes)
	return res, nil
}

// junk is what archivers on macOS and Windows add.
func junk(name string) bool {
	n := strings.ReplaceAll(name, "\\", "/")
	b := path.Base(n)
	return strings.HasPrefix(n, "__MACOSX/") || b == ".DS_Store" || b == "Thumbs.db" || b == "desktop.ini"
}

func (s *Server) registerZipCmds() {
	s.RegisterCmd("unzip", s.cmdUnzip, Writes(),
		Describe(`unpack a .zip: {"path":"/a.zip","to":"/a","onconflict":"rename|overwrite|fail"}; to defaults to a new folder next to it`))
}
