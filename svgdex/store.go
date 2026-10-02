// Package svgdex is a searchable catalogue of SVG files: icon sets are imported into a folder of content-addressed
// SVGs plus one SQLite index (full-text search with prefix matching, ranking, and counts per set / licence), and
// served read-only over HTTP (see server.go).
//
//	st, _ := svgdex.Open("~/.svgdex")            // index.db + svg/ab/abcd….svg
//	st.PutSet(svgdex.Set{ID: "bioicons", Name: "Bioicons", License: "CC-BY-4.0"})
//	st.PutIcon(svgdex.Icon{Set: "bioicons", Key: "cells/neuron.svg", Name: "neuron", Tags: []string{"cell"}}, svg)
//	res, _ := st.Search(svgdex.Query{Q: "neur", Limit: 60})
//
// An icon is a (set, key) pair — the key is its path inside the set — pointing at a blob by content hash, so the same
// SVG in two sets is stored once. Re-importing a set replaces it (BeginSet / CommitSet keep the swap atomic).
package svgdex

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Set is one collection of icons.
type Set struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	License string `json:"license,omitempty"`
	Author  string `json:"author,omitempty"`
	URL     string `json:"url,omitempty"`
	Source  string `json:"source,omitempty"` // where it was imported from (a folder, a file)
	Count   int    `json:"count"`
	// Replace lets an import replace a set of the same id imported from somewhere else (two folders both called
	// "flags"); without it that import is refused.
	Replace bool `json:"-"`
}

// ErrSetTaken: an import would replace a set of the same id imported from another source.
var ErrSetTaken = errors.New("svgdex: a set with this id was imported from elsewhere")

// Icon is one entry of a set. Hash, W, H, Colors, Mono and Bytes are filled in by PutIcon from the SVG itself.
type Icon struct {
	ID        int64    `json:"id"`
	Hash      string   `json:"hash"`
	Set       string   `json:"set"`
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Tags      []string `json:"tags,omitempty"`
	License   string   `json:"license,omitempty"` // empty: the set's
	Author    string   `json:"author,omitempty"`
	SourceURL string   `json:"sourceUrl,omitempty"`
	W         float64  `json:"w"`
	H         float64  `json:"h"`
	Colors    int      `json:"colors"`
	Mono      bool     `json:"mono"`
	Bytes     int      `json:"bytes"`
}

// Store is an open catalogue.
type Store struct {
	dir string
	db  *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sets (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, license TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', updated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS icons (
  id INTEGER PRIMARY KEY, hash TEXT NOT NULL, set_id TEXT NOT NULL, key TEXT NOT NULL, name TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '', license TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '', w REAL NOT NULL DEFAULT 0, h REAL NOT NULL DEFAULT 0,
  colors INTEGER NOT NULL DEFAULT 0, mono INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0,
  UNIQUE(set_id, key)
);
CREATE INDEX IF NOT EXISTS icons_hash ON icons(hash);
CREATE INDEX IF NOT EXISTS icons_set ON icons(set_id, name);
-- Full text over name, tags and the set's name; rowid = icons.id. Tokens split on anything not a letter / digit.
CREATE VIRTUAL TABLE IF NOT EXISTS icons_fts USING fts5(name, tags, setname, tokenize = "unicode61 remove_diacritics 2");
`

// Open opens (creating it if needed) the catalogue in dir: dir/index.db and dir/svg/.
func Open(dir string) (*Store, error) {
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "svg"), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "index.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("svgdex: schema: %w", err)
	}
	return &Store{dir: dir, db: db}, nil
}

// Close closes the index.
func (s *Store) Close() error { return s.db.Close() }

// Dir is the catalogue's folder.
func (s *Store) Dir() string { return s.dir }

// HashOf is the content hash an SVG is stored under (hex sha256, 64 chars).
func HashOf(svg []byte) string { h := sha256.Sum256(svg); return hex.EncodeToString(h[:]) }

// BlobPath is where the SVG with this hash lives (it may not exist).
func (s *Store) BlobPath(hash string) string {
	if len(hash) < 3 {
		return filepath.Join(s.dir, "svg", "_", hash+".svg")
	}
	return filepath.Join(s.dir, "svg", hash[:2], hash+".svg")
}

// ValidHash reports whether h looks like a stored hash (so it is safe in a path).
func ValidHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Store) putBlob(hash string, svg []byte) error {
	p := s.BlobPath(hash)
	if _, err := os.Stat(p); err == nil {
		return nil // content-addressed: already there
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, svg, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// PutSet adds or updates a set's description (its icons are untouched).
func (s *Store) PutSet(set Set) error {
	if set.ID == "" {
		return errors.New("svgdex: a set needs an id")
	}
	if set.Name == "" {
		set.Name = set.ID
	}
	_, err := s.db.Exec(`INSERT INTO sets(id, name, license, author, url, source, updated) VALUES(?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET name=excluded.name, license=excluded.license, author=excluded.author,
	  url=excluded.url, source=excluded.source, updated=excluded.updated`,
		set.ID, set.Name, set.License, set.Author, set.URL, set.Source, time.Now().Unix())
	return err
}

// Batch is a set being (re)imported: icons go in through Put; Commit replaces the set's previous icons with them in
// one transaction (a failed import leaves the old set as it was); Rollback drops the batch.
type Batch struct {
	s       *Store
	tx      *sql.Tx
	set     Set
	n       int
	ins     *sql.Stmt
	insFts  *sql.Stmt
	seenKey map[string]bool
}

// BeginSet starts (re)importing a set: its description is written and its old icons removed — inside the batch's
// transaction, so readers keep seeing the old set until Commit (and a rolled-back import leaves no trace). A set of
// the same id imported from another source is not replaced unless set.Replace.
func (s *Store) BeginSet(set Set) (*Batch, error) {
	if set.ID == "" {
		return nil, errors.New("svgdex: a set needs an id")
	}
	if set.Name == "" {
		set.Name = set.ID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Batch, error) { tx.Rollback(); return nil, err }
	var prev string
	if err := tx.QueryRow(`SELECT source FROM sets WHERE id = ?`, set.ID).Scan(&prev); err == nil && !set.Replace && prev != "" && set.Source != "" && prev != set.Source {
		return fail(fmt.Errorf("%w: %q comes from %s (re-import that, choose another id, or replace it)", ErrSetTaken, set.ID, prev))
	}
	if _, err := tx.Exec(`INSERT INTO sets(id, name, license, author, url, source, updated) VALUES(?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET name=excluded.name, license=excluded.license, author=excluded.author,
	  url=excluded.url, source=excluded.source, updated=excluded.updated`,
		set.ID, set.Name, set.License, set.Author, set.URL, set.Source, time.Now().Unix()); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(`DELETE FROM icons_fts WHERE rowid IN (SELECT id FROM icons WHERE set_id = ?)`, set.ID); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(`DELETE FROM icons WHERE set_id = ?`, set.ID); err != nil {
		return fail(err)
	}
	ins, err := tx.Prepare(`INSERT INTO icons(hash, set_id, key, name, tags, license, author, source_url, w, h, colors, mono, bytes)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fail(err)
	}
	insFts, err := tx.Prepare(`INSERT INTO icons_fts(rowid, name, tags, setname) VALUES(?,?,?,?)`)
	if err != nil {
		return fail(err)
	}
	return &Batch{s: s, tx: tx, set: set, ins: ins, insFts: insFts, seenKey: map[string]bool{}}, nil
}

// Put cleans an SVG (see Clean) and adds it to the batch under its key. A key seen twice keeps the first.
func (b *Batch) Put(ic Icon, svg []byte) (Icon, error) {
	clean, info, err := Clean(svg)
	if err != nil {
		return ic, err
	}
	if ic.Key == "" {
		return ic, errors.New("svgdex: an icon needs a key")
	}
	if b.seenKey[ic.Key] {
		return ic, nil
	}
	b.seenKey[ic.Key] = true
	ic.Set = b.set.ID
	ic.Hash = HashOf(clean)
	ic.W, ic.H, ic.Colors, ic.Mono, ic.Bytes = info.W, info.H, info.Colors, info.Mono, len(clean)
	if ic.Name == "" {
		ic.Name = NameFromKey(ic.Key)
	}
	// Its words: the tags given, the folders it sits in inside the set ("cells/x.svg" → cells), its <title> / <desc>.
	ic.Tags = mergeTags(ic.Tags, strings.Split(path.Dir(ic.Key), "/"), info.Words)
	if err := b.s.putBlob(ic.Hash, clean); err != nil {
		return ic, err
	}
	mono := 0
	if ic.Mono {
		mono = 1
	}
	res, err := b.ins.Exec(ic.Hash, ic.Set, ic.Key, ic.Name, strings.Join(ic.Tags, " "), ic.License, ic.Author, ic.SourceURL,
		ic.W, ic.H, ic.Colors, mono, ic.Bytes)
	if err != nil {
		return ic, err
	}
	ic.ID, _ = res.LastInsertId()
	if _, err := b.insFts.Exec(ic.ID, searchText(ic.Name), searchText(strings.Join(ic.Tags, " ")), searchText(b.set.Name)); err != nil {
		return ic, err
	}
	b.n++
	return ic, nil
}

// Count is how many icons the batch holds so far.
func (b *Batch) Count() int { return b.n }

// Commit makes the batch the set's icons.
func (b *Batch) Commit() error {
	b.ins.Close()
	b.insFts.Close()
	return b.tx.Commit()
}

// Rollback abandons the batch (the set keeps its previous icons).
func (b *Batch) Rollback() error {
	b.ins.Close()
	b.insFts.Close()
	return b.tx.Rollback()
}

// RemoveSet deletes a set and its icons (blobs other sets share stay; unused blobs go with Prune).
func (s *Store) RemoveSet(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM icons_fts WHERE rowid IN (SELECT id FROM icons WHERE set_id = ?)`,
		`DELETE FROM icons WHERE set_id = ?`,
		`DELETE FROM sets WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Prune removes blobs no icon points at; returns how many went.
func (s *Store) Prune() (int, error) {
	used := map[string]bool{}
	rows, err := s.db.Query(`SELECT DISTINCT hash FROM icons`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var h string
		rows.Scan(&h)
		used[h] = true
	}
	rows.Close()
	n := 0
	err = filepath.WalkDir(filepath.Join(s.dir, "svg"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".svg") {
			return nil
		}
		if h := strings.TrimSuffix(filepath.Base(p), ".svg"); !used[h] {
			if os.Remove(p) == nil {
				n++
			}
		}
		return nil
	})
	return n, err
}

// Sets lists every set with its icon count, by name.
func (s *Store) Sets() ([]Set, error) {
	rows, err := s.db.Query(`SELECT s.id, s.name, s.license, s.author, s.url, s.source, COUNT(i.id)
	  FROM sets s LEFT JOIN icons i ON i.set_id = s.id GROUP BY s.id ORDER BY s.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Set{}
	for rows.Next() {
		var x Set
		if err := rows.Scan(&x.ID, &x.Name, &x.License, &x.Author, &x.URL, &x.Source, &x.Count); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Get is one icon by id (its licence / author resolved from the set when its own are empty), or nil.
func (s *Store) Get(id int64) (*Icon, error) {
	rows, err := s.db.Query(selectIcons+` WHERE i.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanIcons(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

const selectIcons = `SELECT i.id, i.hash, i.set_id, i.key, i.name, i.tags,
  CASE WHEN i.license <> '' THEN i.license ELSE s.license END,
  CASE WHEN i.author <> '' THEN i.author ELSE s.author END,
  i.source_url, i.w, i.h, i.colors, i.mono, i.bytes
  FROM icons i JOIN sets s ON s.id = i.set_id`

func scanIcons(rows *sql.Rows) ([]Icon, error) {
	out := []Icon{}
	for rows.Next() {
		var x Icon
		var tags string
		var mono int
		if err := rows.Scan(&x.ID, &x.Hash, &x.Set, &x.Key, &x.Name, &tags, &x.License, &x.Author, &x.SourceURL,
			&x.W, &x.H, &x.Colors, &mono, &x.Bytes); err != nil {
			return nil, err
		}
		x.Mono = mono != 0
		if tags != "" {
			x.Tags = strings.Fields(tags)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
