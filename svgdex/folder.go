package svgdex

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FolderSet is a set's description as a folder may carry it, in set.json at its root:
//
//	{ "id": "bioicons", "name": "Bioicons", "license": "CC-BY-4.0", "author": "…", "url": "https://bioicons.com",
//	  "icons": { "cells/neuron.svg": { "name": "Neuron", "tags": ["nerve"], "license": "CC0-1.0", "author": "…" } } }
//
// Everything is optional; the folder's name is the default id / name. "icons" adds per-file names, tags, licences.
type FolderSet struct {
	Set
	Icons map[string]struct {
		Name      string   `json:"name"`
		Tags      []string `json:"tags"`
		License   string   `json:"license"`
		Author    string   `json:"author"`
		SourceURL string   `json:"sourceUrl"`
	} `json:"icons"`
}

// ImportReport says what an import did.
type ImportReport struct {
	Set     string   `json:"set"`
	Added   int      `json:"added"`
	Skipped []string `json:"skipped,omitempty"` // "path: reason"
	// Entries of a kind svgdex cannot import (draw.io's own shapes), counted.
	Unsupported int `json:"unsupported,omitempty"`
}

// ImportFolder (re)imports every .svg under dir as one set. The set's id / name / licence come from `over` where
// set, then dir/set.json, then the folder's name. Its words: the file name, the sub-folders it sits in, and the
// SVG's own <title> / <desc>. A file that is not usable SVG is skipped (and reported), not fatal.
func (s *Store) ImportFolder(dir string, over Set) (*ImportReport, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("svgdex: %s is not a folder", dir)
	}
	var meta FolderSet
	if b, err := os.ReadFile(filepath.Join(abs, "set.json")); err == nil {
		if err := json.Unmarshal(b, &meta); err != nil {
			return nil, fmt.Errorf("svgdex: %s/set.json: %w", dir, err)
		}
	}
	set := meta.Set
	for _, f := range []struct {
		dst *string
		v   string
	}{{&set.ID, over.ID}, {&set.Name, over.Name}, {&set.License, over.License}, {&set.Author, over.Author}, {&set.URL, over.URL}} {
		if f.v != "" {
			*f.dst = f.v
		}
	}
	if set.ID == "" {
		set.ID = SetID(filepath.Base(abs))
	}
	if set.Name == "" {
		set.Name = filepath.Base(abs)
	}
	set.Source = abs
	batch, err := s.BeginSet(set)
	if err != nil {
		return nil, err
	}
	rep := &ImportReport{Set: set.ID}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != abs && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".svg") {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		key := filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			rep.Skipped = append(rep.Skipped, key+": "+err.Error())
			return nil
		}
		ic := Icon{Key: key}
		if m, ok := meta.Icons[key]; ok {
			ic.Name, ic.License, ic.Author, ic.SourceURL = m.Name, m.License, m.Author, m.SourceURL
			ic.Tags = append(ic.Tags, m.Tags...)
		}
		if _, err := batch.Put(ic, b); err != nil {
			rep.Skipped = append(rep.Skipped, key+": "+err.Error())
		}
		return nil
	})
	if err != nil {
		batch.Rollback()
		return nil, err
	}
	rep.Added = batch.Count()
	if rep.Added == 0 { // nothing usable: no empty set (and an earlier import of it stays)
		return rep, batch.Rollback()
	}
	return rep, batch.Commit()
}

// SetID makes a set id from a name: lower-case words joined by "-".
func SetID(name string) string {
	id := strings.Join(Words(name), "-")
	if id == "" {
		return "set"
	}
	return id
}
