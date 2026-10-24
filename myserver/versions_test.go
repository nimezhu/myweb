package myserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tsrv struct {
	t    *testing.T
	s    *Server
	url  string
	root string
}

func newVersionServer(t *testing.T, cfg Config) *tsrv {
	cfg.Root = t.TempDir()
	s := New(cfg)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return &tsrv{t, s, ts.URL, cfg.Root}
}

func (x *tsrv) do(method, p, body string, hdr map[string]string) (*http.Response, map[string]interface{}) {
	x.t.Helper()
	req, _ := http.NewRequest(method, x.url+p, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var j map[string]interface{}
	json.Unmarshal(data, &j)
	return resp, j
}

func (x *tsrv) cmd(name, body string) (int, map[string]interface{}) {
	r, j := x.do("POST", "/cmd/"+name, body, map[string]string{"Content-Type": "application/json"})
	res, _ := j["result"].(map[string]interface{})
	if res == nil {
		res = j
	}
	return r.StatusCode, res
}

func (x *tsrv) read(p string) string {
	b, err := os.ReadFile(filepath.Join(x.root, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestConditionalWrites(t *testing.T) {
	x := newVersionServer(t, Config{})
	// create-only
	r, j := x.do("POST", "/w/doc.txt", "v1", map[string]string{"If-None-Match": "*"})
	etag1 := r.Header.Get("ETag")
	if r.StatusCode != 200 || etag1 == "" || j["file"].(map[string]interface{})["etag"] != etag1 {
		t.Fatalf("create: %d %v", r.StatusCode, j)
	}
	if r, _ := x.do("POST", "/w/doc.txt", "again", map[string]string{"If-None-Match": "*"}); r.StatusCode != 412 {
		t.Fatalf("create-only on existing: %d", r.StatusCode)
	}
	// the /f/ ETag is the one to send back
	if r, _ := x.do("GET", "/f/doc.txt", "", nil); r.Header.Get("ETag") != etag1 {
		t.Fatal("/f/ and /w/ ETags differ")
	}
	time.Sleep(10 * time.Millisecond)
	r, _ = x.do("POST", "/w/doc.txt", "v2", map[string]string{"If-Match": etag1})
	etag2 := r.Header.Get("ETag")
	if r.StatusCode != 200 || etag2 == etag1 {
		t.Fatalf("matching write: %d", r.StatusCode)
	}
	// a second writer still holding etag1 is refused, and told the current ETag
	r, j = x.do("POST", "/w/doc.txt", "lost update", map[string]string{"If-Match": etag1})
	if r.StatusCode != 412 || j["conflict"] != true || j["etag"] != etag2 || x.read("doc.txt") != "v2" {
		t.Fatalf("stale write: %d %v %q", r.StatusCode, j, x.read("doc.txt"))
	}
	if r, _ := x.do("POST", "/w/new.txt", "x", map[string]string{"If-Match": "*"}); r.StatusCode != 412 {
		t.Fatalf("If-Match * on missing: %d", r.StatusCode)
	}
	if r, _ := x.do("POST", "/rm/doc.txt", "", map[string]string{"If-Match": etag1}); r.StatusCode != 412 || x.read("doc.txt") != "v2" {
		t.Fatalf("stale delete: %d", r.StatusCode)
	}
	if r, _ := x.do("POST", "/rm/doc.txt", "", map[string]string{"If-Match": etag2}); r.StatusCode != 200 {
		t.Fatalf("matching delete: %d", r.StatusCode)
	}
	// unconditional writes keep working as before
	if r, _ := x.do("POST", "/w/doc.txt", "plain", nil); r.StatusCode != 200 {
		t.Fatalf("plain write: %d", r.StatusCode)
	}
}

func TestHistoryAndRestore(t *testing.T) {
	x := newVersionServer(t, Config{KeepVersions: 3})
	for _, v := range []string{"one", "two", "three", "four", "five"} {
		x.do("POST", "/w/notes/a.md", v, nil)
		time.Sleep(5 * time.Millisecond) // distinct mtimes
	}
	code, res := x.cmd("history", `{"path":"/notes/a.md"}`)
	vs, _ := res["versions"].([]interface{})
	if code != 200 || len(vs) != 3 {
		t.Fatalf("history: %d %v", code, res)
	}
	newest := vs[0].(map[string]interface{})
	if r, _ := x.do("GET", newest["url"].(string), "", nil); r.StatusCode != 200 {
		t.Fatal("version not readable via /f/")
	}
	if b, _ := os.ReadFile(filepath.Join(x.root, ".myweb/history/notes/a.md", newest["version"].(string))); string(b) != "four" {
		t.Fatalf("newest version = %q, want four", b)
	}
	// restore the oldest kept ("two"; "one" was pruned); "five" becomes a version
	oldest := vs[2].(map[string]interface{})["version"].(string)
	if code, res := x.cmd("restore", `{"path":"/notes/a.md","version":"`+oldest+`"}`); code != 200 || res["etag"] == nil {
		t.Fatalf("restore: %d %v", code, res)
	}
	if x.read("notes/a.md") != "two" {
		t.Fatalf("after restore: %q", x.read("notes/a.md"))
	}
	_, res = x.cmd("history", `{"path":"/notes/a.md"}`)
	if b, _ := os.ReadFile(filepath.Join(x.root, ".myweb/history/notes/a.md", res["versions"].([]interface{})[0].(map[string]interface{})["version"].(string))); string(b) != "five" {
		t.Fatalf("restore should keep the replaced content, got %q", b)
	}
	// versions are not changed by later edits of the file in place
	x.do("POST", "/w/notes/a.md", "six", nil)
	_, res = x.cmd("history", `{"path":"/notes/a.md"}`)
	for _, v := range res["versions"].([]interface{}) {
		p := filepath.Join(x.root, ".myweb/history/notes/a.md", v.(map[string]interface{})["version"].(string))
		before, _ := os.ReadFile(p)
		os.WriteFile(filepath.Join(x.root, "notes/a.md"), []byte("edited in place"), 0o644)
		if after, _ := os.ReadFile(p); string(after) != string(before) {
			t.Fatalf("version changed with the file: %q -> %q", before, after)
		}
	}
	if code, _ := x.cmd("restore", `{"path":"/notes/a.md","version":"../../../etc"}`); code != 400 {
		t.Fatalf("bad version: %d", code)
	}
	if code, _ := x.cmd("restore", `{"path":"/notes/a.md","version":"nope"}`); code != 404 {
		t.Fatalf("missing version: %d", code)
	}
}

func TestVersionsOffAndSizeCap(t *testing.T) {
	x := newVersionServer(t, Config{KeepVersions: -1})
	x.do("POST", "/w/a", "1", nil)
	x.do("POST", "/w/a", "2", nil)
	if _, res := x.cmd("history", `{"path":"/a"}`); len(res["versions"].([]interface{})) != 0 {
		t.Fatal("versions kept while disabled")
	}
	y := newVersionServer(t, Config{MaxVersionSize: 4})
	y.do("POST", "/w/big", "123456789", nil)
	y.do("POST", "/w/big", "x", nil)
	if _, res := y.cmd("history", `{"path":"/big"}`); len(res["versions"].([]interface{})) != 0 {
		t.Fatal("file over the size cap was versioned")
	}
}

func TestTrash(t *testing.T) {
	x := newVersionServer(t, Config{})
	x.do("POST", "/w/dir/doc.txt", "keep me", nil)
	if r, _ := x.do("POST", "/rm/dir/doc.txt", "", nil); r.StatusCode != 200 || x.read("dir/doc.txt") != "<missing>" {
		t.Fatalf("rm: %d", r.StatusCode)
	}
	code, res := x.cmd("trash", "")
	items, _ := res["items"].([]interface{})
	if code != 200 || len(items) != 1 {
		t.Fatalf("trash: %d %v", code, res)
	}
	it := items[0].(map[string]interface{})
	if it["path"] != "/dir/doc.txt" {
		t.Fatalf("trash item: %v", it)
	}
	if r, _ := x.do("GET", it["url"].(string), "", nil); r.StatusCode != 200 {
		t.Fatal("trashed file not readable")
	}
	// put back; conflicts if the path was re-used
	x.do("POST", "/w/dir/doc.txt", "new one", nil)
	if code, _ := x.cmd("untrash", `{"id":"`+it["id"].(string)+`"}`); code != 409 {
		t.Fatalf("untrash onto existing: %d", code)
	}
	if code, _ := x.cmd("untrash", `{"id":"`+it["id"].(string)+`","overwrite":true}`); code != 200 || x.read("dir/doc.txt") != "keep me" {
		t.Fatalf("untrash overwrite: %d %q", code, x.read("dir/doc.txt"))
	}
	if _, res := x.cmd("history", `{"path":"/dir/doc.txt"}`); len(res["versions"].([]interface{})) == 0 {
		t.Fatal("the overwritten file should be kept as a version")
	}
	x.do("POST", "/rm/dir/doc.txt", "", nil)
	if code, res := x.cmd("emptyTrash", ""); code != 200 || res["removed"].(float64) != 1 {
		t.Fatalf("emptyTrash: %d %v", code, res)
	}
	// removing a missing file is still 404, empty dirs still go
	if r, _ := x.do("POST", "/rm/dir/nope", "", nil); r.StatusCode != 404 {
		t.Fatalf("missing: %d", r.StatusCode)
	}
	if r, _ := x.do("POST", "/rm/dir", "", nil); r.StatusCode != 200 {
		t.Fatalf("empty dir: %d", r.StatusCode)
	}
}

func TestTrashOffAndExpiry(t *testing.T) {
	x := newVersionServer(t, Config{TrashDays: -1})
	x.do("POST", "/w/a", "1", nil)
	x.do("POST", "/rm/a", "", nil)
	if _, res := x.cmd("trash", ""); len(res["items"].([]interface{})) != 0 {
		t.Fatal("trash used while disabled")
	}
	y := newVersionServer(t, Config{TrashDays: 1})
	old := filepath.Join(y.root, ".myweb/trash", time.Now().Add(-48*time.Hour).UTC().Format(versionLayout)+"-abcdef", "old.txt")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("x"), 0o644)
	y.s.pruneTrash()
	if _, err := os.Stat(old); err == nil {
		t.Fatal("expired trash not pruned")
	}
}

func TestReservedFolder(t *testing.T) {
	x := newVersionServer(t, Config{})
	x.do("POST", "/w/a", "1", nil)
	x.do("POST", "/w/a", "2", nil) // creates .myweb/history
	for _, p := range []string{"/w/.myweb/x", "/w/.myweb/history/a/evil", "/rm/.myweb/history"} {
		if r, _ := x.do("POST", p, "x", nil); r.StatusCode != 403 {
			t.Errorf("%s: %d, want 403", p, r.StatusCode)
		}
	}
	r, _ := x.do("GET", "/ls/", "", nil)
	if r.StatusCode != 200 {
		t.Fatal("ls")
	}
	req, _ := http.NewRequest("GET", x.url+"/ls/", nil)
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), ".myweb") {
		t.Fatal(".myweb listed in the root")
	}
}

func TestRmCmd(t *testing.T) {
	x := newVersionServer(t, Config{})
	x.do("POST", "/w/a/one.txt", "1", nil)
	x.do("POST", "/w/a/b/two.txt", "2", nil)
	x.do("POST", "/w/c.txt", "3", nil)
	code, res := x.cmd("rm", `{"paths":["/a","/c.txt"]}`)
	if code != 200 || res["files"].(float64) != 3 {
		t.Fatalf("rm: %d %v", code, res)
	}
	if x.read("a/b/two.txt") != "<missing>" || x.read("c.txt") != "<missing>" {
		t.Fatal("not removed")
	}
	if _, err := os.Stat(filepath.Join(x.root, "a")); !os.IsNotExist(err) {
		t.Fatal("the emptied folder stays")
	}
	_, res = x.cmd("trash", "")
	if items, _ := res["items"].([]interface{}); len(items) != 3 {
		t.Fatalf("trash: %v", res)
	}
	for _, c := range []struct {
		params string
		want   int
	}{
		{`{"paths":["/"]}`, 400},
		{`{"paths":["/.myweb"]}`, 403},
		{`{"paths":["/nope"]}`, 404},
		{`{"paths":[]}`, 400},
	} {
		if code, _ := x.cmd("rm", c.params); code != c.want {
			t.Errorf("%s: %d, want %d", c.params, code, c.want)
		}
	}
}
