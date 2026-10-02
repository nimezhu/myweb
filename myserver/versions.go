package myserver

// Safety for writes and deletes.
//
// Conditional requests: /w/ and /rm/ honour If-Match / If-None-Match against
// the file's ETag (as served by /f/), so a client can refuse to overwrite a
// change it hasn't seen ("412 Precondition Failed").
//
// Versions and trash live in <root>/.myweb/, which clients can read via /f/
// but not write:
//
//	.myweb/history/<path>/<version>   previous contents of <path>, newest kept
//	.myweb/trash/<id>/<path>          deleted files, kept for TrashDays
//
// A version is a hard link to the replaced file (writes always land as a new
// file via rename), so keeping one costs nothing until the file changes.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	metaDir        = ".myweb"
	versionLayout  = "20060102T150405.000000000Z"
	defaultKeep    = 20
	defaultMaxSize = 64 << 20
	defaultTrash   = 30
)

// IsReserved reports whether a URL path is inside the server's own
// .myweb folder, which clients may read but not modify.
func IsReserved(urlPath string) bool {
	p := path.Clean("/" + urlPath)
	return p == "/"+metaDir || strings.HasPrefix(p, "/"+metaDir+"/")
}

// fileETag identifies a file's content by size and modification time, like
// nginx. The same value is served by /f/ and checked by conditional writes.
func fileETag(fi os.FileInfo) string {
	return `"` + strconv.FormatInt(fi.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(fi.Size(), 36) + `"`
}

// pathLocks serializes check-and-write on the same file across requests.
type pathLocks [64]sync.Mutex

func (l *pathLocks) lock(p string) func() {
	h := fnv.New32a()
	h.Write([]byte(p))
	m := &l[h.Sum32()%uint32(len(l))]
	m.Lock()
	return m.Unlock
}

// precondition checks If-Match / If-None-Match against the file at p. It
// returns "" when the request may proceed.
func precondition(r *http.Request, p string) string {
	var current string
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		current = fileETag(fi)
	}
	matches := func(header string) bool {
		for _, t := range strings.Split(header, ",") {
			t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
			if (t == "*" && current != "") || (t != "*" && t == current) {
				return true
			}
		}
		return false
	}
	if h := r.Header.Get("If-Match"); h != "" && !matches(h) {
		if current == "" {
			return "file does not exist"
		}
		return "file changed since it was read"
	}
	if h := r.Header.Get("If-None-Match"); h != "" && matches(h) {
		return "file already exists"
	}
	return ""
}

func writePreconditionFailed(w http.ResponseWriter, p, msg string) {
	resp := map[string]interface{}{"success": 0, "error": msg, "conflict": true}
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		resp["etag"] = fileETag(fi)
		w.Header().Set("ETag", fileETag(fi))
	}
	writeJSON(w, http.StatusPreconditionFailed, resp)
}

func (s *Server) keepVersions() int {
	switch {
	case s.cfg.KeepVersions < 0:
		return 0
	case s.cfg.KeepVersions == 0:
		return defaultKeep
	}
	return s.cfg.KeepVersions
}

func (s *Server) maxVersionSize() int64 {
	if s.cfg.MaxVersionSize > 0 {
		return s.cfg.MaxVersionSize
	}
	return defaultMaxSize
}

func (s *Server) trashDays() int {
	switch {
	case s.cfg.TrashDays < 0:
		return 0
	case s.cfg.TrashDays == 0:
		return defaultTrash
	}
	return s.cfg.TrashDays
}

// Safety reports the effective settings: versions kept per file and days in
// the trash (0 = off).
func (s *Server) Safety() (versions, trashDays int) { return s.keepVersions(), s.trashDays() }

func (s *Server) metaPath(parts ...string) string {
	return filepath.Join(append([]string{s.ResolvePath("/"), metaDir}, parts...)...)
}

func (s *Server) historyDir(urlPath string) string {
	return s.metaPath("history", filepath.FromSlash(strings.TrimPrefix(path.Clean("/"+urlPath), "/")))
}

// KeepVersion saves the current content of urlPath (if it is a file) into
// its history before it is replaced. Plugins that overwrite files should call
// it first. Failures are logged, not fatal: a missed version must not block
// the user's save. It returns the version file ("" if none was kept), which
// the caller should remove if the replacement then fails: until the file is
// replaced, the version shares its content.
func (s *Server) KeepVersion(urlPath string) string {
	keep := s.keepVersions()
	if keep == 0 || IsReserved(urlPath) {
		return ""
	}
	p := s.ResolvePath(urlPath)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > s.maxVersionSize() {
		return ""
	}
	dir := s.historyDir(urlPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf("history %s: %v", urlPath, err)
		return ""
	}
	name := fi.ModTime().UTC().Format(versionLayout)
	dst := filepath.Join(dir, name)
	for i := 1; fileExists(dst); i++ {
		dst = filepath.Join(dir, name+"-"+strconv.Itoa(i))
	}
	if err := os.Link(p, dst); err != nil { // e.g. a filesystem without hard links
		if err := copyFile(p, dst); err != nil {
			logf("history %s: %v", urlPath, err)
			return ""
		}
	}
	s.pruneVersions(dir, keep)
	return dst
}

func (s *Server) pruneVersions(dir string, keep int) {
	vs := listVersions(dir)
	for _, v := range vs[min(keep, len(vs)):] {
		os.Remove(filepath.Join(dir, v.Name()))
	}
}

// listVersions returns version files, newest first.
func listVersions(dir string) []os.FileInfo {
	entries, _ := os.ReadDir(dir)
	var vs []os.FileInfo
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			vs = append(vs, fi)
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Name() > vs[j].Name() })
	return vs
}

// trashFile moves a file into the trash (or deletes it when trash is off).
func (s *Server) trashFile(urlPath string) error {
	p := s.ResolvePath(urlPath)
	if s.trashDays() == 0 {
		return os.Remove(p)
	}
	b := make([]byte, 3)
	rand.Read(b)
	id := time.Now().UTC().Format(versionLayout) + "-" + hex.EncodeToString(b)
	dst := s.metaPath("trash", id, filepath.FromSlash(strings.TrimPrefix(path.Clean("/"+urlPath), "/")))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(p, dst); err != nil {
		os.RemoveAll(s.metaPath("trash", id))
		return err
	}
	go s.pruneTrash()
	return nil
}

type trashItem struct {
	ID   string    `json:"id"`
	Path string    `json:"path"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
	URL  string    `json:"url"`
}

func (s *Server) trashItems() []trashItem {
	base := s.metaPath("trash")
	ids, _ := os.ReadDir(base)
	var items []trashItem
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		t, err := time.Parse(versionLayout, strings.SplitN(id.Name(), "-", 2)[0])
		if err != nil {
			continue
		}
		dir := filepath.Join(base, id.Name())
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			fi, _ := d.Info()
			var size int64
			if fi != nil {
				size = fi.Size()
			}
			urlRel := "/" + filepath.ToSlash(rel)
			items = append(items, trashItem{id.Name(), urlRel, t, size,
				"/f/" + metaDir + "/trash/" + id.Name() + urlRel})
			return filepath.SkipAll // one file per trash entry
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items
}

var pruneMu sync.Mutex

func (s *Server) pruneTrash() {
	days := s.trashDays()
	if days == 0 {
		return
	}
	pruneMu.Lock()
	defer pruneMu.Unlock()
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	base := s.metaPath("trash")
	ids, _ := os.ReadDir(base)
	for _, id := range ids {
		t, err := time.Parse(versionLayout, strings.SplitN(id.Name(), "-", 2)[0])
		if err == nil && t.Before(cutoff) {
			os.RemoveAll(filepath.Join(base, id.Name()))
		}
	}
}

// ---- commands: history, restore, trash, untrash, emptyTrash ----

func (s *Server) registerSafetyCmds() {
	s.RegisterCmd("history", s.cmdHistory,
		Describe(`previous versions of a file, newest first: {"path":"/a.txt"}`))
	s.RegisterCmd("restore", s.cmdRestore, Writes(),
		Describe(`make a previous version current (the current one is kept): {"path":"/a.txt","version":"…"}`))
	s.RegisterCmd("trash", s.cmdTrash,
		Describe(`deleted files, newest first`))
	s.RegisterCmd("untrash", s.cmdUntrash, Writes(),
		Describe(`put a deleted file back: {"id":"…","overwrite":false}`))
	s.RegisterCmd("emptyTrash", s.cmdEmptyTrash, Writes(),
		Describe(`permanently delete everything in the trash`))
}

type versionInfo struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
	Size    int64     `json:"size"`
	URL     string    `json:"url"`
}

func (s *Server) cmdHistory(_ context.Context, req *CmdRequest) (interface{}, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	clean := path.Clean("/" + p.Path)
	if clean == "/" || IsReserved(clean) {
		return nil, Errorf(http.StatusBadRequest, "path must be a file")
	}
	out := []versionInfo{}
	for _, fi := range listVersions(s.historyDir(clean)) {
		t, _ := time.Parse(versionLayout, strings.SplitN(fi.Name(), "-", 2)[0])
		out = append(out, versionInfo{fi.Name(), t, fi.Size(),
			"/f/" + metaDir + "/history" + clean + "/" + fi.Name()})
	}
	return map[string]interface{}{"path": clean, "versions": out}, nil
}

func (s *Server) cmdRestore(_ context.Context, req *CmdRequest) (interface{}, error) {
	var p struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	clean := path.Clean("/" + p.Path)
	if clean == "/" || IsReserved(clean) || p.Version == "" || strings.ContainsAny(p.Version, `/\`) || p.Version == ".." {
		return nil, Errorf(http.StatusBadRequest, "path and version are required")
	}
	src := filepath.Join(s.historyDir(clean), p.Version)
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, Errorf(http.StatusNotFound, "no version %s of %s", p.Version, clean)
	}
	dst := s.ResolvePath(clean)
	defer s.locks.lock(dst)()
	kept := s.KeepVersion(clean)
	// a copy, never a link: the history entry must not change with the file
	if err := writeFileAtomic(dst, data); err != nil {
		if kept != "" {
			os.Remove(kept)
		}
		return nil, err
	}
	res := map[string]interface{}{"path": clean, "restored": p.Version}
	if fi, err := os.Stat(dst); err == nil {
		res["etag"] = fileETag(fi)
	}
	return res, nil
}

func (s *Server) cmdTrash(_ context.Context, req *CmdRequest) (interface{}, error) {
	items := s.trashItems()
	if items == nil {
		items = []trashItem{}
	}
	return map[string]interface{}{"items": items, "days": s.trashDays()}, nil
}

func (s *Server) cmdUntrash(_ context.Context, req *CmdRequest) (interface{}, error) {
	var p struct {
		ID        string `json:"id"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.ID == "" || strings.ContainsAny(p.ID, `/\`) || p.ID == ".." {
		return nil, Errorf(http.StatusBadRequest, "id is required")
	}
	var item *trashItem
	for _, it := range s.trashItems() {
		if it.ID == p.ID {
			it := it
			item = &it
		}
	}
	if item == nil {
		return nil, Errorf(http.StatusNotFound, "not in trash: %s", p.ID)
	}
	dst := s.ResolvePath(item.Path)
	defer s.locks.lock(dst)()
	if fileExists(dst) {
		if !p.Overwrite {
			return nil, Errorf(http.StatusConflict, "%s exists again (set overwrite to replace it)", item.Path)
		}
		s.KeepVersion(item.Path)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	src := s.metaPath("trash", p.ID, filepath.FromSlash(strings.TrimPrefix(item.Path, "/")))
	if err := os.Rename(src, dst); err != nil {
		return nil, err
	}
	os.RemoveAll(s.metaPath("trash", p.ID))
	return map[string]string{"path": item.Path}, nil
}

func (s *Server) cmdEmptyTrash(_ context.Context, req *CmdRequest) (interface{}, error) {
	n := len(s.trashItems())
	if err := os.RemoveAll(s.metaPath("trash")); err != nil {
		return nil, err
	}
	return map[string]int{"removed": n}, nil
}

// ---- helpers ----

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

var errReserved = errors.New(metaDir + " is managed by the server")

func logf(format string, a ...interface{}) { log.Printf(format, a...) }
