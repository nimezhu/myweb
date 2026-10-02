package plugins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/myserver"
)

func run(t *testing.T, url, name, body string) (int, map[string]interface{}) {
	t.Helper()
	resp, err := http.Post(url+"/cmd/"+name, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&j)
	return resp.StatusCode, j
}

func TestMv(t *testing.T) {
	root := t.TempDir()
	srv := myserver.New(myserver.Config{Root: root})
	RegisterFileOps(srv)
	ts := httptest.NewServer(srv.Mux())
	defer ts.Close()

	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, p)); return string(b) }
	write("a.txt", "A")
	write("b.txt", "B")
	write("dir/x.txt", "X")

	cases := []struct {
		name, body string
		want       int
	}{
		{"rename", `{"from":"/a.txt","to":"/a2.txt"}`, 200},
		{"into new folder", `{"from":"/a2.txt","to":"/new/deep/a.txt"}`, 200},
		{"missing", `{"from":"/nope","to":"/x"}`, 404},
		{"exists", `{"from":"/b.txt","to":"/new/deep/a.txt"}`, 409},
		{"root", `{"from":"/","to":"/x"}`, 400},
		{"same", `{"from":"/b.txt","to":"/b.txt"}`, 400},
		{"into itself", `{"from":"/dir","to":"/dir/sub"}`, 400},
		{"escape stays in root", `{"from":"/b.txt","to":"/../../escaped.txt"}`, 200},
		{"overwrite dir refused", `{"from":"/escaped.txt","to":"/dir","overwrite":true}`, 409},
		{"move dir", `{"from":"/dir","to":"/moved"}`, 200},
	}
	for _, c := range cases {
		if code, j := run(t, ts.URL, "mv", c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, j, c.want)
		}
	}
	if read("new/deep/a.txt") != "A" || read("escaped.txt") != "B" || read("moved/x.txt") != "X" {
		t.Fatal("files not where expected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); err == nil {
		t.Fatal("mv escaped root")
	}
	write("c.txt", "C")
	if code, _ := run(t, ts.URL, "mv", `{"from":"/c.txt","to":"/escaped.txt","overwrite":true}`); code != 200 || read("escaped.txt") != "C" {
		t.Fatal("overwrite file failed")
	}
	srv.SetReadOnly(true)
	if code, _ := run(t, ts.URL, "mv", `{"from":"/escaped.txt","to":"/z.txt"}`); code != 403 {
		t.Fatalf("read-only mv: %d", code)
	}
}
