package myserver

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// zipEntries downloads p and returns its files' names and contents.
func zipEntries(t *testing.T, x *tsrv, p string) (map[string]string, *http.Response) {
	t.Helper()
	resp, err := http.Get(x.url + p)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, resp
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			out[f.Name] = "<dir>"
			continue
		}
		rc, _ := f.Open()
		c, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(c)
	}
	return out, resp
}

func keys(m map[string]string) string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return strings.Join(k, " ")
}

func TestZipDownload(t *testing.T) {
	x := newVersionServer(t, Config{})
	writeFiles(t, x.root, map[string]string{
		"docs/a.md": "A", "docs/sub/b.txt": "B", "docs/c.png": "C", "top.txt": "T",
	})
	x.do("POST", "/w/docs/a.md", "A2", nil) // the old A goes to .myweb/history

	got, resp := zipEntries(t, x, "/zip/docs")
	if keys(got) != "a.md c.png sub/ sub/b.txt" || got["a.md"] != "A2" || got["sub/b.txt"] != "B" {
		t.Fatalf("folder: %s %v", keys(got), got)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "docs.zip") {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// the root: everything but the server's own folder
	got, _ = zipEntries(t, x, "/zip/")
	if strings.Contains(keys(got), metaDir) || got["top.txt"] != "T" || got["docs/sub/b.txt"] != "B" {
		t.Fatalf("root: %s", keys(got))
	}
	// a selection, with names that try to climb out
	got, _ = zipEntries(t, x, "/zip/docs?f=a.md&f=sub&f=../top.txt&f=missing")
	if keys(got) != "a.md sub/ sub/b.txt" {
		t.Fatalf("selection: %s", keys(got))
	}
	got, resp = zipEntries(t, x, "/zip/docs?f=sub")
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "sub.zip") {
		t.Fatalf("one entry: Content-Disposition = %q", cd)
	}
	for _, p := range []string{"/zip/nope", "/zip/top.txt", "/zip/" + metaDir} {
		if _, resp := zipEntries(t, x, p); resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	zw.Close()
	return b.Bytes()
}

func TestUnzip(t *testing.T) {
	x := newVersionServer(t, Config{})
	arc := makeZip(t, map[string]string{
		"a.txt": "A", "sub/b.txt": "B", "../../evil.txt": "E", "/abs.txt": "Z",
		"__MACOSX/._a.txt": "junk", "sub/.DS_Store": "junk",
	})
	os.WriteFile(filepath.Join(x.root, "data.zip"), arc, 0o644)

	st, j := x.cmd("unzip", `{"path":"/data.zip"}`)
	if st != 200 || j["to"] != "/data" || j["files"] != 4.0 {
		t.Fatalf("unzip: %d %v", st, j)
	}
	for name, want := range map[string]string{"data/a.txt": "A", "data/sub/b.txt": "B", "data/evil.txt": "E", "data/abs.txt": "Z"} {
		if got := x.read(name); got != want {
			t.Errorf("%s = %q", name, got)
		}
	}
	for _, name := range []string{"evil.txt", "data/__MACOSX", "data/sub/.DS_Store"} {
		if _, err := os.Stat(filepath.Join(x.root, name)); err == nil {
			t.Errorf("%s was unpacked", name)
		}
	}

	// again: a new folder, not mixed into the first
	if st, j := x.cmd("unzip", `{"path":"/data.zip"}`); st != 200 || j["to"] != "/data (1)" {
		t.Fatalf("second unzip: %d %v", st, j)
	}
	// into an existing folder: rename, fail, overwrite
	os.WriteFile(filepath.Join(x.root, "data", "a.txt"), []byte("mine"), 0o644)
	if st, j := x.cmd("unzip", `{"path":"/data.zip","to":"/data"}`); st != 200 || j["renamed"] != 4.0 || x.read("data/a (1).txt") != "A" {
		t.Fatalf("rename: %d %v", st, j)
	}
	if st, _ := x.cmd("unzip", `{"path":"/data.zip","to":"/data","onconflict":"fail"}`); st != 409 {
		t.Fatalf("fail: %d", st)
	}
	if st, j := x.cmd("unzip", `{"path":"/data.zip","to":"/data","onconflict":"overwrite"}`); st != 200 || x.read("data/a.txt") != "A" {
		t.Fatalf("overwrite: %d %v", st, j)
	}
	if st, j := x.cmd("history", `{"path":"/data/a.txt"}`); st != 200 || !strings.Contains(x.readHistory(j), "mine") {
		t.Fatalf("the overwritten file is not in its history: %v", j)
	}

	for body, want := range map[string]int{
		`{"path":"/missing.zip"}`:                  404,
		`{"path":"/data/a.txt"}`:                   400,
		`{"path":"/data.zip","to":"/.myweb/x"}`:    403,
		`{"path":"/data.zip","onconflict":"skip"}`: 400,
	} {
		if st, j := x.cmd("unzip", body); st != want {
			t.Errorf("%s: %d %v, want %d", body, st, j, want)
		}
	}
	x.s.cfg.ReadOnly = true
	x.s.readOnly.Store(true)
	if st, _ := x.cmd("unzip", `{"path":"/data.zip"}`); st != 403 {
		t.Errorf("read-only: %d", st)
	}
}

func TestUnzipLimits(t *testing.T) {
	x := newVersionServer(t, Config{})
	os.WriteFile(filepath.Join(x.root, "big.zip"), makeZip(t, map[string]string{"a": strings.Repeat("x", 1000), "b": "y"}), 0o644)
	defer func(b int64, f int) { MaxUnzipBytes, MaxUnzipFiles = b, f }(MaxUnzipBytes, MaxUnzipFiles)
	MaxUnzipBytes = 500
	if st, j := x.cmd("unzip", `{"path":"/big.zip"}`); st != 413 {
		t.Fatalf("bytes: %d %v", st, j)
	}
	MaxUnzipBytes, MaxUnzipFiles = 1<<20, 1
	if st, j := x.cmd("unzip", `{"path":"/big.zip","to":"/b2"}`); st != 413 {
		t.Fatalf("files: %d %v", st, j)
	}
}

// readHistory reads every version listed by the history command.
func (x *tsrv) readHistory(j map[string]interface{}) string {
	var out strings.Builder
	list, _ := j["versions"].([]interface{})
	for _, v := range list {
		u, _ := v.(map[string]interface{})["url"].(string)
		resp, err := http.Get(x.url + u)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			out.Write(b)
		}
	}
	return out.String()
}

func TestZipMultiUser(t *testing.T) {
	m := newMultiUser(t)
	entries := func(who, p string) (int, string) {
		st, body := m.do(who, "GET", p, nil, "")
		if st != 200 {
			return st, ""
		}
		zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		sort.Strings(names)
		return st, strings.Join(names, " ")
	}
	if _, got := entries("bob", "/zip/"); got != "projects/x/ projects/x/plan.md users/bob/ users/bob/b.txt" {
		t.Errorf("bob's root zip: %q", got)
	}
	if st, _ := entries("bob", "/zip/users/alice"); st != 404 {
		t.Errorf("bob zips alice's folder: %d", st)
	}
	if st, _ := entries("", "/zip/projects"); st != 401 && st != 404 {
		t.Errorf("anonymous: %d", st)
	}
	os.WriteFile(filepath.Join(m.dir, "users/bob/a.zip"), makeZip(t, map[string]string{"x.txt": "x"}), 0o644)
	cmd := func(who, body string) int {
		st, _ := m.do(who, "POST", "/cmd/unzip", strings.NewReader(body), "application/json")
		return st
	}
	if st := cmd("bob", `{"path":"/users/bob/a.zip","to":"/users/alice/x"}`); st != 403 {
		t.Errorf("bob unzips into alice's folder: %d", st)
	}
	if st := cmd("vic", `{"path":"/users/bob/a.zip","to":"/users/vic/x"}`); st != 403 {
		t.Errorf("vic reads bob's zip: %d", st)
	}
	if st := cmd("bob", `{"path":"/users/bob/a.zip"}`); st != 200 {
		t.Errorf("bob unzips his own: %d", st)
	}
}
