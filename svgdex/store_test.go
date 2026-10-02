package svgdex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func svg(fill string) []byte {
	return []byte(`<svg viewBox="0 0 24 24"><path fill="` + fill + `" d="M0 0h24v24z"/></svg>`)
}

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func put(t *testing.T, st *Store, set Set, icons map[string][]byte, tags map[string][]string) {
	t.Helper()
	b, err := st.BeginSet(set)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range icons {
		if _, err := b.Put(Icon{Key: k, Tags: tags[k]}, v); err != nil {
			t.Fatal(k, err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
}

func names(r *Result) string {
	var n []string
	for _, i := range r.Items {
		n = append(n, i.Name)
	}
	return strings.Join(n, ",")
}

func TestSearchPrefixRankFiltersFacets(t *testing.T) {
	st := newStore(t)
	put(t, st, Set{ID: "bio", Name: "Bioicons", License: "CC-BY-4.0"}, map[string][]byte{
		"cells/mitochondrion.svg":  svg("#f00"),
		"cells/neuron.svg":         svg("#000"),
		"lab/pipette.svg":          svg("#000"),
		"cells/mitochondria-2.svg": []byte(`<svg viewBox="0 0 1 1"><path fill="#f00"/><path fill="#0f0"/></svg>`),
	}, map[string][]string{"lab/pipette.svg": {"liquid handling"}})
	put(t, st, Set{ID: "ui", Name: "UI icons", License: "MIT"}, map[string][]byte{"arrow-left.svg": svg("#000"), "mito.svg": svg("#000")}, nil)

	r, err := st.Search(Query{Q: "mito"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 3 || r.Items[0].Name != "mito" {
		t.Errorf("prefix + exact name first: %d %s", r.Total, names(r))
	}
	if r, _ = st.Search(Query{Q: "liq"}); names(r) != "pipette" {
		t.Errorf("tags are searched: %s", names(r))
	}
	if r, _ = st.Search(Query{Q: "bioicons neu"}); names(r) != "neuron" {
		t.Errorf("the set's name and AND of words: %s", names(r))
	}
	if r, _ = st.Search(Query{Q: "cells"}); r.Total != 3 {
		t.Errorf("sub-folder words: %d", r.Total)
	}
	if r, _ = st.Search(Query{Q: "mito", Sets: []string{"bio"}}); r.Total != 2 {
		t.Errorf("set filter: %d", r.Total)
	}
	if r, _ = st.Search(Query{Q: "mito", License: "mit"}); names(r) != "mito" {
		t.Errorf("licence filter (case-insensitive, from the set): %s", names(r))
	}
	if r, _ = st.Search(Query{Q: "mito", Style: "color"}); names(r) != "mitochondria 2" {
		t.Errorf("style filter: %s", names(r))
	}
	r, _ = st.Search(Query{Q: "mito", Sets: []string{"bio"}, Facets: true})
	if r.Facets["set"]["bio"] != 2 || r.Facets["set"]["ui"] != 1 || r.Facets["license"]["CC-BY-4.0"] != 2 || r.Facets["style"]["mono"] != 1 {
		t.Errorf("facets (a dimension ignores its own filter): %v", r.Facets)
	}
	r, _ = st.Search(Query{Limit: 4})
	if r.Total != 6 || len(r.Items) != 4 || r.Next != 4 || r.Items[0].Set != "bio" {
		t.Errorf("browse + paging: total %d items %d next %d", r.Total, len(r.Items), r.Next)
	}
	if r, _ = st.Search(Query{Q: `"); DROP TABLE icons; --`}); r == nil {
		t.Error("odd input is just words")
	}
	if sug, _ := st.Suggest("mi", 5); len(sug) == 0 || !strings.HasPrefix(sug[0], "mi") {
		t.Errorf("suggest: %v", sug)
	}
}

func TestBlobsAreSharedAndReimportReplaces(t *testing.T) {
	st := newStore(t)
	same := svg("#123456")
	put(t, st, Set{ID: "a"}, map[string][]byte{"x.svg": same}, nil)
	put(t, st, Set{ID: "b"}, map[string][]byte{"y.svg": same}, nil)
	r, _ := st.Search(Query{})
	if r.Total != 2 || r.Items[0].Hash != r.Items[1].Hash {
		t.Fatalf("one blob for the same SVG: %+v", r.Items)
	}
	b, err := os.ReadFile(st.BlobPath(r.Items[0].Hash))
	if err != nil || !strings.Contains(string(b), "#123456") {
		t.Fatalf("blob on disk: %v", err)
	}
	// re-importing "a" with another icon replaces its old one
	put(t, st, Set{ID: "a"}, map[string][]byte{"z.svg": svg("#000")}, nil)
	if r, _ = st.Search(Query{Sets: []string{"a"}}); names(r) != "z" {
		t.Errorf("re-import replaces: %s", names(r))
	}
	// a rolled-back import leaves the set as it was
	bt, _ := st.BeginSet(Set{ID: "a"})
	bt.Put(Icon{Key: "w.svg"}, svg("#111"))
	bt.Rollback()
	if r, _ = st.Search(Query{Sets: []string{"a"}}); names(r) != "z" {
		t.Errorf("rollback keeps the old set: %s", names(r))
	}
	// the same id from another source is refused — unless replacing is asked for; the same source re-imports
	put(t, st, Set{ID: "c", Source: "/one"}, map[string][]byte{"c.svg": svg("#222")}, nil)
	if bt, err := st.BeginSet(Set{ID: "c", Source: "/two"}); err == nil {
		bt.Rollback()
		t.Error("a set from another source is not replaced silently")
	}
	for _, set := range []Set{{ID: "c", Source: "/two", Replace: true}, {ID: "c", Source: "/one"}} {
		if bt, err := st.BeginSet(set); err != nil {
			t.Errorf("%+v: %v", set, err)
		} else {
			bt.Rollback()
		}
	}
	st.RemoveSet("c")
	if err := st.RemoveSet("b"); err != nil {
		t.Fatal(err)
	}
	n, _ := st.Prune()
	if n < 1 {
		t.Errorf("prune removes unused blobs: %d", n)
	}
	sets, _ := st.Sets()
	if len(sets) != 1 || sets[0].ID != "a" || sets[0].Count != 1 {
		t.Errorf("sets: %+v", sets)
	}
	if ic, _ := st.Get(r.Items[0].ID); ic == nil || ic.Name != "z" {
		t.Errorf("get: %+v", ic)
	}
}

func TestImportFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Icons")
	os.MkdirAll(filepath.Join(dir, "animals", ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, "animals", "cat.svg"), svg("#000"), 0o644)
	os.WriteFile(filepath.Join(dir, "animals", ".hidden", "x.svg"), svg("#000"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.svg"), []byte("nope"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "set.json"), []byte(`{"license":"CC0-1.0","icons":{"animals/cat.svg":{"name":"House cat","tags":["pet"]}}}`), 0o644)
	st := newStore(t)
	rep, err := st.ImportFolder(dir, Set{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Set != "my-icons" || rep.Added != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	r, _ := st.Search(Query{Q: "pet anim"})
	if names(r) != "House cat" || r.Items[0].License != "CC0-1.0" {
		t.Errorf("set.json names / tags / licence, folder words: %+v", r.Items)
	}
}

func TestSearchFindsOtherForms(t *testing.T) {
	st := newStore(t)
	put(t, st, Set{ID: "bio", Name: "Bioicons"}, map[string][]byte{
		"mitochondrium.svg": svg("#000"), "antibody.svg": svg("#000"), "cell.svg": svg("#000"), "mitochondrial-dna.svg": svg("#000"),
	}, nil)
	for q, want := range map[string]int{"mitochondria": 2, "antibodies": 1, "cells": 1, "bioicons cells": 1, "antibody": 1} {
		r, err := st.Search(Query{Q: q})
		if err != nil || r.Total != want {
			t.Errorf("%q: %v total %d, want %d", q, err, r.Total, want)
		}
	}
}
