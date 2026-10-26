package myserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestDu(t *testing.T) {
	x := newVersionServer(t, Config{})
	writeFiles(t, x.root, map[string]string{
		"top.txt":       "12345",
		"a/one.txt":     "1",
		"a/b/two.txt":   "22",
		"photos/x.jpg":  "xxxxxxxxxx",
		"empty/.keep":   "",
		".myweb/x/junk": "not counted",
	})
	x.do("POST", "/w/top.txt", "123456", nil) // the old one is kept in the history
	code, res := x.cmd("du", `{"path":"/"}`)
	if code != 200 {
		t.Fatalf("du: %d %v", code, res)
	}
	if res["size"].(float64) != 6+1+2+10 || res["files"].(float64) != 5 {
		t.Fatalf("in all: %v", res)
	}
	f := res["folders"].(map[string]interface{})
	if a := f["a"].(map[string]interface{}); a["size"].(float64) != 3 || a["files"].(float64) != 2 || a["dirs"].(float64) != 1 {
		t.Fatalf("a: %v", a)
	}
	if _, ok := f["empty"]; !ok || f[".myweb"] != nil || f["top.txt"] != nil {
		t.Fatalf("folders: %v", f)
	}
	if kept := res["kept"].(map[string]interface{}); kept["history"].(float64) != 5 {
		t.Fatalf("kept: %v", kept)
	}
	if code, res := x.cmd("du", `{"path":"/a"}`); code != 200 || res["size"].(float64) != 3 || res["kept"] != nil {
		t.Fatalf("du /a: %d %v", code, res)
	}
	for _, p := range []string{"/nope", "/top.txt", "/.myweb"} {
		if code, _ := x.cmd("du", `{"path":"`+p+`"}`); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
	_, st := x.do("GET", "/cmd/status", "", nil)
	disk, _ := st["disk"].(map[string]interface{})
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if disk == nil || disk["total"].(float64) <= 0 || disk["free"].(float64) > disk["total"].(float64) {
			t.Fatalf("status disk: %v", st["disk"])
		}
	}
}

func TestDuAccess(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"users/alice/a.txt": "AAAA", "users/bob/b.txt": "BB", "projects/x/p.txt": "P", "secret/s.txt": "SSS"})
	s := New(Config{Root: root, Access: newTestAccess()})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	du := func(who, p string) (int, map[string]interface{}) {
		req, _ := http.NewRequest("POST", ts.URL+"/cmd/du", strings.NewReader(`{"path":"`+p+`"}`))
		req.Header.Set("Authorization", "Bearer tok-"+who)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var j struct {
			Result map[string]interface{} `json:"result"`
		}
		json.NewDecoder(r.Body).Decode(&j)
		return r.StatusCode, j.Result
	}
	if code, res := du("alice", "/"); code != 200 || res["size"].(float64) != 5 || res["kept"] != nil {
		t.Fatalf("alice sees her files and projects/x only: %d %v", code, res)
	}
	if code, res := du("root", "/"); code != 200 || res["size"].(float64) != 10 || res["kept"] == nil {
		t.Fatalf("root: %d %v", code, res)
	}
	if code, _ := du("bob", "/users/alice"); code != 404 {
		t.Fatalf("bob: %d", code)
	}
}
