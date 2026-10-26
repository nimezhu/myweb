package myserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCp(t *testing.T) {
	x := newVersionServer(t, Config{})
	writeFiles(t, x.root, map[string]string{
		"a.txt":         "A",
		"b.txt":         "B",
		"dir/one.txt":   "1",
		"dir/sub/2.txt": "2",
		"dir/empty/.k":  "",
	})
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(x.root, "dir/one.txt"), old, old)

	if code, res := x.cmd("cp", `{"from":"/a.txt","to":"/c.txt"}`); code != 200 || res["to"] != "/c.txt" || x.read("c.txt") != "A" {
		t.Fatalf("cp file: %d %v", code, res)
	}
	if code, _ := x.cmd("cp", `{"from":"/a.txt","to":"/b.txt"}`); code != http.StatusConflict {
		t.Fatalf("a file there: %d", code)
	}
	if code, res := x.cmd("cp", `{"from":"/a.txt","to":"/a.txt","rename":true}`); code != 200 || res["to"] != "/a (1).txt" || x.read("a (1).txt") != "A" {
		t.Fatalf("duplicate: %d %v", code, res)
	}
	if code, _ := x.cmd("cp", `{"from":"/a.txt","to":"/b.txt","overwrite":true}`); code != 200 || x.read("b.txt") != "A" {
		t.Fatalf("overwrite: %d", code)
	}
	if _, res := x.cmd("history", `{"path":"/b.txt"}`); len(res["versions"].([]interface{})) != 1 {
		t.Fatal("the replaced file is kept as a version")
	}

	code, res := x.cmd("cp", `{"from":"/dir","to":"/copy/dir"}`)
	if code != 200 || res["files"].(float64) != 3 || x.read("copy/dir/sub/2.txt") != "2" {
		t.Fatalf("cp folder: %d %v", code, res)
	}
	if fi, err := os.Stat(filepath.Join(x.root, "copy/dir/one.txt")); err != nil || !fi.ModTime().Equal(old) {
		t.Fatal("a copy keeps the modification time")
	}
	if code, res := x.cmd("cp", `{"from":"/dir","to":"/dir","rename":true}`); code != 200 || res["to"] != "/dir (1)" || x.read("dir (1)/one.txt") != "1" {
		t.Fatalf("duplicate a folder: %d %v", code, res)
	}
	if left, _ := filepath.Glob(filepath.Join(x.root, "copy/dir/.*.tmp*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	for params, want := range map[string]int{
		`{"from":"/dir","to":"/dir/in"}`:                    400,
		`{"from":"/dir","to":"/copy/dir","overwrite":true}`: 409,
		`{"from":"/a.txt","to":"/dir","overwrite":true}`:    409,
		`{"from":"/nope","to":"/x"}`:                        404,
		`{"from":"/.myweb","to":"/x"}`:                      403,
		`{"from":"/a.txt","to":"/.myweb/a"}`:                403,
		`{"from":"/","to":"/x"}`:                            400,
		`{"from":"/a.txt","to":"/a.txt"}`:                   400,
	} {
		if code, res := x.cmd("cp", params); code != want {
			t.Errorf("%s: %d %v, want %d", params, code, res, want)
		}
	}
}

func TestCpAccess(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"users/alice/a.txt": "A", "projects/x/p.txt": "P", "users/bob/b.txt": "B"})
	s := New(Config{Root: root, Access: newTestAccess()})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	cp := func(who, body string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/cmd/cp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-"+who)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := cp("alice", `{"from":"/projects/x/p.txt","to":"/users/alice/p.txt"}`); c != 200 {
		t.Fatalf("alice may copy what she reads into her folder: %d", c)
	}
	if c := cp("bob", `{"from":"/projects/x/p.txt","to":"/users/bob/p.txt"}`); c != 200 {
		t.Fatalf("bob reads projects/x: %d", c)
	}
	if c := cp("bob", `{"from":"/users/alice/a.txt","to":"/users/bob/a.txt"}`); c != 403 {
		t.Fatalf("bob can't read alice's: %d", c)
	}
	if c := cp("vic", `{"from":"/projects/x/p.txt","to":"/users/vic/p.txt"}`); c != 403 {
		t.Fatalf("a viewer writes nothing: %d", c)
	}
	if c := cp("bob", `{"from":"/users/bob/b.txt","to":"/projects/x/b.txt"}`); c != 403 {
		t.Fatalf("bob can't write projects/x: %d", c)
	}
}
