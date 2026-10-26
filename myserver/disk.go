package myserver

// What takes the disk: "du" adds up a folder (and each folder in it), and
// /cmd/status says how full the disk is.

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// duMaxEntries stops a walk of a huge tree; the result says it is partial.
var duMaxEntries = 2_000_000

type duSize struct {
	Size  int64 `json:"size"`
	Files int   `json:"files"`
	Dirs  int   `json:"dirs,omitempty"`
}

type duResult struct {
	Path    string            `json:"path"`
	duSize                    // in all
	Folders map[string]duSize `json:"folders"`           // each folder directly in it, by name
	Kept    map[string]int64  `json:"kept,omitempty"`    // for the whole data folder: history, trash, thumbs
	Partial bool              `json:"partial,omitempty"` // stopped early
	Took    float64           `json:"took"`              // seconds
}

func (s *Server) registerDiskCmds() {
	s.RegisterCmd("du", s.cmdDu,
		Describe(`the size of a folder and of each folder in it (files the user may read): {"path":"/photos"}`))
}

func (s *Server) cmdDu(ctx context.Context, req *CmdRequest) (interface{}, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	start := time.Now()
	clean := path.Clean("/" + p.Path)
	if IsReserved(clean) || !req.Visible(clean, true) {
		return nil, Errorf(http.StatusNotFound, "not found: %s", clean)
	}
	base := s.ResolvePath(clean)
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return nil, Errorf(http.StatusNotFound, "not a folder: %s", clean)
	}
	res := duResult{Path: clean, Folders: map[string]duSize{}}
	entries := 0
	err := filepath.WalkDir(base, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || fp == base {
			return nil // skip what can't be read
		}
		if entries++; entries > duMaxEntries {
			res.Partial = true
			return fs.SkipAll
		}
		if entries%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(base, fp)
		rel = filepath.ToSlash(rel)
		urlPath := path.Join(clean, rel)
		if d.IsDir() {
			if IsReserved(urlPath) || !req.Visible(urlPath, true) {
				return filepath.SkipDir
			}
		} else if !d.Type().IsRegular() || !req.Can(ActRead, urlPath) {
			return nil
		}
		top, _, _ := strings.Cut(rel, "/")
		sz := res.Folders[top]
		if d.IsDir() {
			res.Dirs++
			if top != rel {
				sz.Dirs++
				res.Folders[top] = sz
			} else {
				res.Folders[top] = sz // a folder in it, maybe empty
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		res.Size += fi.Size()
		res.Files++
		if top != rel { // in a folder of it, not a file of its own
			sz.Size += fi.Size()
			sz.Files++
			res.Folders[top] = sz
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if clean == "/" && req.Can(ActAdmin, "/") {
		res.Kept = map[string]int64{}
		for _, k := range []string{"history", "trash", "thumbs"} {
			res.Kept[k] = treeSize(s.metaPath(k))
		}
	}
	res.Took = time.Since(start).Round(time.Millisecond).Seconds()
	return res, nil
}

// treeSize adds up the files under dir.
func treeSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// diskInfo is the data folder's disk, for /cmd/status.
func (s *Server) diskInfo() map[string]uint64 {
	free, total, err := DiskSpace(s.ResolvePath("/"))
	if err != nil || total == 0 {
		return nil
	}
	return map[string]uint64{"free": free, "total": total}
}

func (s *Server) diskIfAuthed(r *http.Request) map[string]uint64 {
	if !s.Authorized(r) {
		return nil
	}
	return s.diskInfo()
}
