package myserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

type appTest struct {
	t    *testing.T
	s    *Server
	url  string
	port string
	root string
}

func newAppTest(t *testing.T) *appTest {
	root := t.TempDir()
	for p, body := range map[string]string{
		"demo/a.txt": "mine", "shared/s.txt": "shared", "secret/x.txt": "secret", "top.txt": "top",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	web := fstest.MapFS{"index.html": {Data: []byte("the app")}}
	mgr := fstest.MapFS{"index.html": {Data: []byte("the manager")}}
	s := New(Config{Root: root, WebFS: http.FS(web), Manager: http.FS(mgr), CORS: DefaultCORS, App: &AppAccess{
		Name:    "demo",
		Folders: func() []Folder { return []Folder{{Path: "/demo"}, {Path: "/shared", ReadOnly: true}} },
	}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	s.port, _ = strconv.Atoi(u.Port())
	return &appTest{t, s, ts.URL, u.Port(), root}
}

// as makes a request the way a page of origin would (origin "" for a program).
func (a *appTest) as(origin, method, p string, host string) (int, string, http.Header) {
	a.t.Helper()
	req, _ := http.NewRequest(method, a.url+p, strings.NewReader("data"))
	if host != "" {
		req.Host = host + ":" + a.port
	}
	if origin != "" {
		if method == "GET" && strings.HasSuffix(origin, req.Host) { // same-origin GETs carry no Origin
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		} else {
			req.Header.Set("Origin", origin)
		}
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestAppLimited(t *testing.T) {
	a := newAppTest(t)
	app := "http://127.0.0.1:" + a.port
	mgr := "http://localhost:" + a.port

	// the app's pages: its folders only
	for p, want := range map[string]int{
		"/f/demo/a.txt": 200, "/f/shared/s.txt": 200, "/f/secret/x.txt": 404, "/f/top.txt": 404,
	} {
		if st, _, _ := a.as(app, "GET", p, "127.0.0.1"); st != want {
			t.Errorf("app GET %s: %d, want %d", p, st, want)
		}
	}
	if _, body, _ := a.as(app, "GET", "/ls/", "127.0.0.1"); !strings.Contains(body, "demo") || !strings.Contains(body, "shared") ||
		strings.Contains(body, "secret") || strings.Contains(body, "top.txt") {
		t.Errorf("app lists /: %s", body)
	}
	for p, want := range map[string]int{
		"/w/demo/new.txt": 200, "/w/shared/n.txt": 403, "/w/top.txt": 403, "/rm/secret/x.txt": 403, "/cmd/stopServer": 403,
	} {
		if st, body, _ := a.as(app, "POST", p, "127.0.0.1"); st != want {
			t.Errorf("app POST %s: %d %s, want %d", p, st, body, want)
		}
	}
	// the app may not reach around through the manager's address either
	if st, _, _ := a.as(app, "GET", "/f/secret/x.txt", "localhost"); st != 404 {
		t.Errorf("app via the manager's host: %d", st)
	}
	if st, _, _ := a.as(app, "POST", "/w/x.txt", "localhost"); st != 403 {
		t.Errorf("app writes via the manager's host: %d", st)
	}
	// so may pages of sites that CORS doesn't allow, and sandboxed frames
	for _, o := range []string{"https://evil.example", "null"} {
		if st, _, _ := a.as(o, "GET", "/f/secret/x.txt", "127.0.0.1"); st != 404 {
			t.Errorf("%s reads a secret: %d", o, st)
		}
	}

	// not limited: the file manager, programs, the user typing an address,
	// sites allowed by CORS
	if st, body, _ := a.as(mgr, "GET", "/f/secret/x.txt", "localhost"); st != 200 || body != "secret" {
		t.Errorf("manager: %d %s", st, body)
	}
	if st, _, _ := a.as(mgr, "POST", "/w/top2.txt", "localhost"); st != 200 {
		t.Errorf("manager writes: %d", st)
	}
	if st, _, _ := a.as("", "GET", "/f/secret/x.txt", ""); st != 200 {
		t.Errorf("curl: %d", st)
	}
	req, _ := http.NewRequest("GET", a.url+"/f/secret/x.txt", nil)
	req.Header.Set("Sec-Fetch-Site", "none")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("typed address: %v %v", resp, err)
	}
	if st, _, _ := a.as("https://youdata.studio", "GET", "/f/secret/x.txt", "127.0.0.1"); st != 200 {
		t.Errorf("CORS site: %d", st)
	}

	// the manager lives at localhost; its old address redirects there
	st, _, h := a.as("", "GET", "/_myweb/?x=1", "127.0.0.1")
	if st != http.StatusTemporaryRedirect || h.Get("Location") != mgr+"/_myweb/?x=1" {
		t.Errorf("manager at 127.0.0.1: %d %s", st, h.Get("Location"))
	}
	st, body, h := a.as("", "GET", "/_myweb/", "localhost")
	if st != 200 || body != "the manager" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("manager at localhost: %d %q %v", st, body, h)
	}
	if st, body, _ := a.as(app, "GET", "/", "127.0.0.1"); st != 200 || body != "the app" {
		t.Errorf("the app: %d %q", st, body)
	}
	if got := a.s.ManagerURL(); got != mgr+"/_myweb/" {
		t.Errorf("ManagerURL = %s", got)
	}
}

func TestAppAllows(t *testing.T) {
	f := []Folder{{Path: "/a"}, {Path: "/b", ReadOnly: true}, {Path: "/b/w"}}
	for _, c := range []struct {
		act  Action
		p    string
		want bool
	}{
		{ActRead, "/a", true}, {ActWrite, "/a/x/y", true}, {ActRead, "/ab", false},
		{ActRead, "/b/x", true}, {ActWrite, "/b/x", false}, {ActWrite, "/b/w/x", true},
		{ActRead, "/", false}, {ActAdmin, "/a", false}, {ActRead, "/../a", true},
		{ActRead, "/" + metaDir + "/history/a/x.txt/1", true}, {ActRead, "/" + metaDir + "/history/c/x.txt/1", false},
		{ActWrite, "/" + metaDir + "/history/a/x.txt/1", false},
	} {
		if got := appAllows(f, c.act, c.p); got != c.want {
			t.Errorf("%v %s = %v", c.act, c.p, got)
		}
	}
	if !appTraverses(f, "/") || appTraverses(f, "/a") || appTraverses(f, "/c") || !appTraverses(f, "/b") {
		t.Error("traverse")
	}
	if !appAllows([]Folder{{Path: "/"}}, ActWrite, "/anything") {
		t.Error("the whole folder, when granted")
	}
}

func TestAppCantBorrowManagerHost(t *testing.T) {
	a := newAppTest(t)
	os.WriteFile(filepath.Join(a.root, "demo", "evil.html"), []byte("<script>fetch('/f/secret/x.txt')</script>"), 0o644)
	os.WriteFile(filepath.Join(a.root, "demo", "noext"), []byte("<html><script>1</script></html>"), 0o644)
	os.WriteFile(filepath.Join(a.root, "demo", "pic.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644)

	// the app's pages are only served at the app's address
	st, _, h := a.as("", "GET", "/index.html", "localhost")
	if st != http.StatusTemporaryRedirect || h.Get("Location") != "http://127.0.0.1:"+a.port+"/index.html" {
		t.Errorf("app page at the manager's host: %d %s", st, h.Get("Location"))
	}
	// data files that run scripts are sandboxed there
	for p, sandboxed := range map[string]bool{"/f/demo/evil.html": true, "/f/demo/noext": true, "/f/demo/pic.png": false} {
		_, _, h := a.as("", "GET", p, "localhost")
		if got := strings.Contains(h.Get("Content-Security-Policy"), "sandbox"); got != sandboxed {
			t.Errorf("%s at the manager's host: CSP %q", p, h.Get("Content-Security-Policy"))
		}
		if strings.Contains(h.Get("Content-Security-Policy"), "allow-same-origin") {
			t.Errorf("%s: sandbox keeps the origin", p)
		}
	}
	// and at the app's address too: there they would act as the app at "/"
	if _, _, h := a.as("", "GET", "/f/demo/evil.html", "127.0.0.1"); !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("app host: CSP %q", h.Get("Content-Security-Policy"))
	}
}
