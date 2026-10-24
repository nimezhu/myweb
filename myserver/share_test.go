package myserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestShareUploadQuota(t *testing.T) {
	st := newShareTest(t, Config{})
	sh, secret, err := st.s.CreateShare("/drop", ShareOptions{Upload: true, MaxMB: 1, MaxFiles: 2})
	if err != nil || sh.MaxBytes != 1<<20 {
		t.Fatalf("%+v %v", sh, err)
	}
	send := func(files map[string]int) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for name, n := range files {
			fw, _ := mw.CreateFormFile("files", name)
			fw.Write(bytes.Repeat([]byte("x"), n))
		}
		mw.Close()
		req, _ := http.NewRequest("POST", st.url+"/_s/"+secret+"/", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := send(map[string]int{"big.bin": 2 << 20}); code != 413 || !strings.Contains(body, "can still take") {
		t.Errorf("over the bytes: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(st.root, "drop", "big.bin")); err == nil {
		t.Error("a file over the quota landed")
	}
	if code, _ := send(map[string]int{"a.bin": 100}); code != 200 {
		t.Errorf("first: %d", code)
	}
	if code, _ := send(map[string]int{"b.bin": 100}); code != 200 {
		t.Errorf("second: %d", code)
	}
	if code, body := send(map[string]int{"c.bin": 100}); code != 413 || !strings.Contains(body, "taken all it can") {
		t.Errorf("over the files: %d %s", code, body)
	}
	now := st.s.shareByID(sh.ID)
	if now.Files != 2 || now.Bytes != 200 {
		t.Errorf("counted %d files, %d bytes", now.Files, now.Bytes)
	}
	if left, _ := filepath.Glob(filepath.Join(st.root, "drop", ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestSharePassword(t *testing.T) {
	st := newShareTest(t, Config{})
	_, res := st.share(`{"path":"/photos","password":"open sesame"}`, "")
	base := st.url + st.path(res["url"].(string))
	jar := &cookieJar{}
	c := http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	get := func(u string) (int, string) {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	unlock := func(pw string) (int, string) {
		resp, err := c.PostForm(base+"?unlock", map[string][]string{"password": {pw}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(base + "a.jpg"); code != 401 || strings.Contains(body, "jpeg") {
		t.Fatalf("without the password: %d %s", code, body)
	}
	if code, _ := unlock("wrong"); code != 401 {
		t.Errorf("a wrong password: %d", code)
	}
	if code, body := unlock("open sesame"); code != 200 || !strings.Contains(body, "a.jpg") {
		t.Errorf("the password: %d %s", code, body)
	}
	if code, body := get(base + "sub/b.txt"); code != 200 || body != "bee" {
		t.Errorf("after it: %d %s", code, body)
	}
	// the cookie of one link doesn't open another
	_, res2 := st.share(`{"path":"/notes.txt","password":"other one"}`, "")
	if code, _ := get(st.url + st.path(res2["url"].(string)) + "notes.txt"); code != 401 {
		t.Errorf("another link: %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(st.root, ".myweb", "shares.json"))
	if strings.Contains(string(b), "open sesame") {
		t.Error("the password is stored")
	}
	// too many wrong ones
	c.Jar = &cookieJar{}
	for range 10 {
		unlock("nope")
	}
	if code, _ := unlock("open sesame"); code != http.StatusTooManyRequests {
		t.Errorf("after 10 wrong: %d", code)
	}
	if code, _ := st.share(`{"path":"/photos","password":"abc"}`, ""); code != 400 {
		t.Errorf("a short password: %d", code)
	}
}

func TestShareDownloadsAndCopy(t *testing.T) {
	st := newShareTest(t, Config{})
	_, res := st.share(`{"path":"/photos","maxDownloads":2}`, "")
	base := st.url + st.path(res["url"].(string))
	get := func(u string, hdr ...string) int {
		req, _ := http.NewRequest("GET", u, nil)
		if len(hdr) == 2 {
			req.Header.Set(hdr[0], hdr[1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	get(base) // a listing isn't a download
	get(base + "a.jpg")
	get(base+"a.jpg", "Range", "bytes=2-3") // the rest of the same download
	if code := get(base + "?zip"); code != 200 {
		t.Fatalf("zip: %d", code)
	}
	if code := get(base + "a.jpg"); code != http.StatusGone {
		t.Errorf("after 2 downloads: %d", code)
	}
	// the list says how many, and gives the link again
	req, _ := http.NewRequest("POST", st.url+"/cmd/shares", nil)
	resp, _ := http.DefaultClient.Do(req)
	var out struct {
		Result []map[string]interface{} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Result) != 1 || out.Result[0]["downloads"].(float64) != 2 || out.Result[0]["url"] != res["url"] {
		t.Errorf("list: %v", out.Result)
	}
	for _, k := range []string{"secret", "hash", "passHash", "passSalt"} {
		if _, ok := out.Result[0][k]; ok {
			t.Errorf("the list shows %s", k)
		}
	}
}

// cookieJar keeps cookies by path, enough for these tests.
type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.cookies = append(j.cookies, cs...) }
func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range j.cookies {
		if strings.HasPrefix(u.Path, c.Path) {
			out = append(out, c)
		}
	}
	return out
}
