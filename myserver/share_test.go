package myserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type shareTest struct {
	t    *testing.T
	s    *Server
	url  string
	root string
}

func newShareTest(t *testing.T, cfg Config) *shareTest {
	root := t.TempDir()
	for p, body := range map[string]string{
		"photos/a.jpg": "jpeg", "photos/sub/b.txt": "bee", "photos/page.html": "<script>alert(1)</script>",
		"secret/x.txt": "secret", "drop/old.txt": "old", "notes.txt": "notes",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	cfg.Root = root
	s := New(cfg)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &shareTest{t, s, ts.URL, root}
}

func (st *shareTest) share(body string, token string) (int, map[string]interface{}) {
	st.t.Helper()
	req, _ := http.NewRequest("POST", st.url+"/cmd/shareCreate", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result map[string]interface{} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Result
}

// path is the link's path on the test server.
func (st *shareTest) path(link string) string {
	return link[strings.Index(link, "/_s/"):]
}

func (st *shareTest) get(p string) (int, string, http.Header) {
	st.t.Helper()
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(st.url + p)
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestShareFolder(t *testing.T) {
	st := newShareTest(t, Config{})
	os.Symlink(filepath.Join(st.root, "secret"), filepath.Join(st.root, "photos", "link"))
	code, res := st.share(`{"path":"/photos","days":3}`, "")
	if code != 200 || res["url"] == nil {
		t.Fatalf("create: %d %v", code, res)
	}
	link := st.path(res["url"].(string))

	st1, body, h := st.get(link)
	if st1 != 200 || !strings.Contains(body, "a.jpg") || !strings.Contains(body, "sub/") || strings.Contains(body, "x.txt") {
		t.Fatalf("listing: %d %s", st1, body)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("listing headers: %v", h)
	}
	for p, want := range map[string]int{
		"a.jpg": 200, "sub/b.txt": 200, "sub/": 200, "link/x.txt": 404, "../secret/x.txt": 404, "%2e%2e/notes.txt": 404,
		".myweb/shares.json": 404,
	} {
		code, body, h := st.get(link + p)
		if code == http.StatusMovedPermanently { // the mux cleans "..", out of the link
			code, body, _ = st.get(h.Get("Location"))
		}
		if code != want || (want == 404 && (strings.Contains(body, "secret") || strings.Contains(body, "notes"))) {
			t.Errorf("GET %s: %d %q, want %d", p, code, body, want)
		}
	}
	if _, _, h := st.get(link + "page.html"); !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("a shared page runs as the server: %v", h)
	}
	// the whole folder as a zip, without the link out
	code, body, _ = st.get(link + "?zip")
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if code != 200 || err != nil {
		t.Fatalf("zip: %d %v", code, err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "a.jpg") || strings.Contains(got, "x.txt") {
		t.Errorf("zip: %s", got)
	}
	// nothing outside the link: other links, writes
	if code, _, _ := st.get("/_s/nope/"); code != 404 {
		t.Errorf("unknown link: %d", code)
	}
	resp, _ := http.Post(st.url+link+"new.txt", "text/plain", strings.NewReader("x"))
	if resp.StatusCode == 200 {
		t.Error("wrote through a read link")
	}

	// listed without the secret; revoked, it is gone
	list, _ := st.s.Shares()
	if len(list) != 1 || list[0].Path != "/photos" || list[0].ID != res["id"] || strings.Contains(link, list[0].Hash) {
		t.Fatalf("shares: %+v", list)
	}
	if err := st.s.RevokeShare(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := st.get(link); code != 404 {
		t.Errorf("revoked: %d", code)
	}
}

func TestShareFileAndLimits(t *testing.T) {
	st := newShareTest(t, Config{Token: "tok"})
	if code, _ := st.share(`{"path":"/notes.txt"}`, ""); code != 401 {
		t.Fatalf("without the token: %d", code)
	}
	for _, bad := range []string{`{"path":"/.myweb"}`, `{"path":"/nope"}`, `{"path":"/notes.txt","upload":true}`, `{"path":"/notes.txt","days":999}`} {
		if code, _ := st.share(bad, "tok"); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, res := st.share(`{"path":"/notes.txt"}`, "tok")
	if code != 200 {
		t.Fatalf("create: %d", code)
	}
	link := st.path(res["url"].(string))
	if code, _, h := st.get(link); code != http.StatusFound || !strings.HasSuffix(h.Get("Location"), link+"notes.txt") {
		t.Errorf("file link: %d %s", code, h.Get("Location"))
	}
	if code, body, _ := st.get(link + "notes.txt"); code != 200 || body != "notes" {
		t.Errorf("file: %d %q", code, body)
	}
	if code, _, _ := st.get(link + "other.txt"); code != 404 {
		t.Errorf("beside the file: %d", code)
	}
	// works without the token: the link is the key
	// expired: gone
	b, _ := os.ReadFile(st.s.sharesFile())
	os.WriteFile(st.s.sharesFile(), bytes.Replace(b, []byte(`"expires": "20`), []byte(`"expires": "19`), 1), 0o644)
	if code, _, _ := st.get(link + "notes.txt"); code != 404 {
		t.Errorf("expired: %d", code)
	}
	if redactShare(link+"notes.txt") != "/_s/…/notes.txt" {
		t.Errorf("redact: %s", redactShare(link))
	}
}

func TestShareUpload(t *testing.T) {
	st := newShareTest(t, Config{})
	_, res := st.share(`{"path":"/drop","upload":true}`, "")
	link := st.path(res["url"].(string))
	code, body, _ := st.get(link)
	if code != 200 || !strings.Contains(body, `type="file"`) || strings.Contains(body, "old.txt") {
		t.Fatalf("form: %d %s", code, body)
	}
	if _, body, _ := st.get(link + "old.txt"); strings.Contains(body, "old") && !strings.Contains(body, "form") {
		t.Fatal("an upload link shows files")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range map[string]string{"old.txt": "new", "../../escape.txt": "x", ".myweb": "y"} {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write([]byte(data))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", st.url+link, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", st.url)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload: %v %v", resp, err)
	}
	if b, _ := os.ReadFile(filepath.Join(st.root, "drop", "old.txt")); string(b) != "old" {
		t.Error("overwrote a file")
	}
	for _, f := range []string{"old (1).txt", "escape.txt", "_.myweb"} {
		if _, err := os.Stat(filepath.Join(st.root, "drop", f)); err != nil {
			t.Errorf("%s not received: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(st.root, "escape.txt")); err == nil {
		t.Error("escaped the folder")
	}
	// another site can't post into it
	req, _ = http.NewRequest("POST", st.url+link, strings.NewReader(""))
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode == 200 {
		t.Error("cross-site upload")
	}
}

func TestAppCantShare(t *testing.T) {
	a := newAppTest(t)
	req, _ := http.NewRequest("POST", a.url+"/cmd/shareCreate", strings.NewReader(`{"path":"/demo"}`))
	req.Header.Set("Origin", "http://127.0.0.1:"+a.port)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 403 {
		t.Errorf("app made a share link: %v %v", resp.Status, err)
	}
}
