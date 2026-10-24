package myserver

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type davTest struct {
	t    *testing.T
	s    *Server
	url  string
	root string
}

func newDAVTest(t *testing.T, cfg Config) *davTest {
	root := t.TempDir()
	for p, body := range map[string]string{"a.txt": "one", "docs/b.txt": "bee", "docs/sub/c.txt": "sea"} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, metaDir), 0o755)
	os.WriteFile(filepath.Join(root, metaDir, "shares.json"), []byte("[]"), 0o644)
	cfg.Root = root
	s := New(cfg)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &davTest{t, s, ts.URL, root}
}

// do sends a WebDAV request; pw, if set, is the Basic password.
func (d *davTest) do(method, p, body, pw string, hdr ...string) (int, string, http.Header) {
	d.t.Helper()
	req, _ := http.NewRequest(method, d.url+p, strings.NewReader(body))
	if pw != "" {
		req.SetBasicAuth("me", pw)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (d *davTest) read(p string) string {
	b, err := os.ReadFile(filepath.Join(d.root, p))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestDAV(t *testing.T) {
	d := newDAVTest(t, Config{})
	// list: no .myweb
	code, body, _ := d.do("PROPFIND", "/dav/", "", "", "Depth", "1")
	if code != 207 || !strings.Contains(body, "a.txt") || !strings.Contains(body, "docs") || strings.Contains(body, metaDir) {
		t.Fatalf("PROPFIND: %d %s", code, body)
	}
	if code, _, _ := d.do("GET", "/dav/.myweb/shares.json", "", ""); code != 404 {
		t.Errorf("GET .myweb: %d", code)
	}
	if code, _, _ := d.do("PUT", "/dav/.myweb/x", "x", ""); code < 400 {
		t.Errorf("PUT into .myweb: %d", code)
	}
	if code, body, _ := d.do("GET", "/dav/docs/b.txt", "", ""); code != 200 || body != "bee" {
		t.Errorf("GET: %d %q", code, body)
	}

	// write: new, then replaced (the old one kept as a version)
	if code, _, _ := d.do("PUT", "/dav/new.txt", "fresh", ""); code != 201 || d.read("new.txt") != "fresh" {
		t.Errorf("PUT new: %d %s", code, d.read("new.txt"))
	}
	if code, _, _ := d.do("PUT", "/dav/a.txt", "two", ""); code >= 300 || d.read("a.txt") != "two" {
		t.Errorf("PUT over: %d %s", code, d.read("a.txt"))
	}
	if vs := listVersions(d.s.historyDir("/a.txt")); len(vs) != 1 {
		t.Errorf("versions of a.txt: %d", len(vs))
	}
	if left, _ := filepath.Glob(filepath.Join(d.root, ".*.dav*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// folders: make, move, delete (into the trash, file by file)
	if code, _, _ := d.do("MKCOL", "/dav/made", "", ""); code != 201 {
		t.Errorf("MKCOL: %d", code)
	}
	if code, _, _ := d.do("MOVE", "/dav/new.txt", "", "", "Destination", d.url+"/dav/made/moved.txt"); code != 201 || d.read("made/moved.txt") != "fresh" {
		t.Errorf("MOVE: %d", code)
	}
	if code, _, _ := d.do("DELETE", "/dav/docs/", "", ""); code != 204 {
		t.Errorf("DELETE folder: %d", code)
	}
	if _, err := os.Stat(filepath.Join(d.root, "docs")); !os.IsNotExist(err) {
		t.Error("the folder is still there")
	}
	got := map[string]bool{}
	for _, it := range d.s.trashItems() {
		got[it.Path] = true
	}
	if !got["/docs/b.txt"] || !got["/docs/sub/c.txt"] {
		t.Errorf("trash: %v", got)
	}
	if code, _, _ := d.do("DELETE", "/dav/", "", ""); code < 400 {
		t.Errorf("DELETE of everything: %d", code)
	}

	// read-only mode refuses changes, not reads
	d.s.SetReadOnly(true)
	if code, _, _ := d.do("PUT", "/dav/ro.txt", "x", ""); code != 403 {
		t.Errorf("read-only PUT: %d", code)
	}
	if code, _, _ := d.do("PROPFIND", "/dav/", "", "", "Depth", "0"); code != 207 {
		t.Errorf("read-only PROPFIND: %d", code)
	}
}

func TestDAVCutShort(t *testing.T) {
	d := newDAVTest(t, Config{})
	conn, err := net.Dial("tcp", strings.TrimPrefix(d.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("PUT /dav/a.txt HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\npartial"))
	conn.Close()
	time.Sleep(300 * time.Millisecond)
	if got := d.read("a.txt"); got != "one" {
		t.Errorf("a cut-short PUT replaced the file: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(d.root, ".*.dav*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestDAVAuth(t *testing.T) {
	d := newDAVTest(t, Config{Token: "tok"})
	code, _, h := d.do("PUT", "/dav/x.txt", "x", "")
	if code != 401 || !strings.Contains(h.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("PUT without the password: %d %v", code, h)
	}
	if code, _, _ := d.do("PUT", "/dav/x.txt", "x", "wrong"); code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	if code, _, _ := d.do("PUT", "/dav/x.txt", "x", "tok"); code != 201 {
		t.Errorf("with the token as password: %d", code)
	}
	if code, _, _ := d.do("PROPFIND", "/dav/", "", "", "Depth", "0"); code != 207 {
		t.Errorf("local reads need no password: %d", code)
	}
	// the Basic password works only for /dav/
	if code, _, _ := d.do("POST", "/w/y.txt", "y", "tok"); code != 401 {
		t.Errorf("Basic auth outside /dav/: %d", code)
	}
	g := newDAVTest(t, Config{Token: "tok", Global: true})
	if code, _, _ := g.do("PROPFIND", "/dav/", "", "", "Depth", "0"); code != 401 {
		t.Errorf("-global reads without the password: %d", code)
	}
	if off := newDAVTest(t, Config{NoDAV: true}); true {
		if code, _, _ := off.do("PROPFIND", "/dav/", "", "", "Depth", "0"); code == 207 {
			t.Error("NoDAV still serves it")
		}
	}
}

func TestDAVNotForApps(t *testing.T) {
	a := newAppTest(t)
	req, _ := http.NewRequest("PROPFIND", a.url+"/dav/", nil)
	req.Header.Set("Origin", "http://127.0.0.1:"+a.port)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 403 {
		t.Errorf("an app's page used WebDAV: %v %v", resp.Status, err)
	}
}
