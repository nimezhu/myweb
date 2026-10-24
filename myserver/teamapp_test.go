package myserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// newTeamAppTest: a multi-user server (testAccess) with an app on a port of
// its own that may use /projects/x and /users/alice/app.
func newTeamAppTest(t *testing.T) (*Server, int, int) {
	root := t.TempDir()
	for p, body := range map[string]string{
		"users/alice/a.txt": "alice private", "users/alice/app/s.txt": "alice in app",
		"projects/x/plan.md": "shared", "secret/s.txt": "secret",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	s := New(Config{Root: root, Port: freePort(t), Access: newTestAccess(), CORS: DefaultCORS,
		Manager: http.FS(fstest.MapFS{"index.html": {Data: []byte("the manager")}})})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown(t.Context()) })
	port, err := s.Mount(&Mount{
		Access: &AppAccess{Name: "demo", Folders: func() []Folder {
			return []Folder{{Path: "/projects/x"}, {Path: "/users/alice/app"}}
		}},
		FS: http.FS(fstest.MapFS{"index.html": {Data: []byte("the app")}}),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Port(), port
}

// treq asks port as who; how: "app" (a page of the app, with Origin),
// "old" (a browser that sends neither Origin nor Sec-Fetch-Site), or ""
// (a program).
func treq(t *testing.T, method string, port int, p, who, how string, appPort int) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+p, strings.NewReader("data"))
	if who != "" {
		req.AddCookie(&http.Cookie{Name: "myweb_token_" + strconv.Itoa(port), Value: "tok-" + who})
		req.Header.Set("Authorization", "Bearer tok-"+who)
	}
	switch how {
	case "app":
		req.Header.Set("Origin", "http://127.0.0.1:"+strconv.Itoa(appPort))
	case "old":
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Safari/605.1.15")
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestTeamApp(t *testing.T) {
	s, main, app := newTeamAppTest(t)
	type c struct {
		method, port, p, who, how string
		want                      int
	}
	ports := map[string]int{"main": main, "app": app}
	for _, x := range []c{
		// alice from the app's pages: her rights, within the app's folders
		{"GET", "app", "/f/projects/x/plan.md", "alice", "app", 200},
		{"GET", "app", "/f/users/alice/app/s.txt", "alice", "app", 200},
		{"GET", "app", "/f/users/alice/a.txt", "alice", "app", 404}, // hers, not the app's
		{"GET", "app", "/f/secret/s.txt", "alice", "app", 404},
		{"POST", "app", "/w/projects/x/new.md", "alice", "app", 200},
		{"POST", "app", "/w/users/alice/a2.txt", "alice", "app", 403},
		// and asking the main port from the app's pages changes nothing
		{"GET", "main", "/f/users/alice/a.txt", "alice", "app", 404},
		// bob: the app's folders, within his rights (he only reads /projects/x)
		{"GET", "app", "/f/projects/x/plan.md", "bob", "app", 200},
		{"POST", "app", "/w/projects/x/bob.md", "bob", "app", 403},
		{"GET", "app", "/f/users/alice/app/s.txt", "bob", "app", 404},
		// nobody signed in
		{"GET", "app", "/f/projects/x/plan.md", "", "app", 401},
		// alice at the file manager: all her rights
		{"GET", "main", "/f/users/alice/a.txt", "alice", "", 200},
		// a browser without Origin or Sec-Fetch-Site (plain http on a LAN):
		// the app's port is the app's
		{"GET", "app", "/f/users/alice/a.txt", "alice", "old", 404},
		{"GET", "main", "/f/users/alice/a.txt", "alice", "old", 200},
	} {
		code, body, _ := treq(t, x.method, ports[x.port], x.p, x.who, x.how, app)
		if code != x.want {
			t.Errorf("%s %s %s as %s (%s): %d %s, want %d", x.method, x.port, x.p, x.who, x.how, code, body, x.want)
		}
	}
	// the app is at its port; the manager at "/" of the main port
	code, _, h := treq(t, "GET", main, "/apps/demo/?x=1", "alice", "", app)
	if code != http.StatusTemporaryRedirect || h.Get("Location") != "http://127.0.0.1:"+strconv.Itoa(app)+"/?x=1" {
		t.Errorf("/apps/demo/: %d %s", code, h.Get("Location"))
	}
	if _, body, _ := treq(t, "GET", app, "/", "alice", "", app); body != "the app" {
		t.Errorf("app port /: %q", body)
	}
	_ = s
}

// a page's Referer, from old browsers, tells whose page it is
func TestOldBrowserReferer(t *testing.T) {
	a := newAppTest(t)
	get := func(host, referer string) int {
		req, _ := http.NewRequest("GET", a.url+"/f/secret/x.txt", nil)
		req.Host = host + ":" + a.port
		req.Header.Set("User-Agent", "Mozilla/5.0 Safari/605.1.15")
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		host, referer string
		want          int
	}{
		{"127.0.0.1", "http://127.0.0.1:" + a.port + "/index.html", 404}, // the app's page
		{"127.0.0.1", "", 404}, // the app's address
		{"localhost", "http://localhost:" + a.port + "/_myweb/", 200}, // the file manager
		{"localhost", "", 200},                         // typed at the manager's address
		{"127.0.0.1", "https://evil.example/", 404},    // another site
		{"127.0.0.1", "https://youdata.studio/x", 200}, // a CORS site
	} {
		if got := get(c.host, c.referer); got != c.want {
			t.Errorf("host %s referer %q: %d, want %d", c.host, c.referer, got, c.want)
		}
	}
}

func TestDataPagesSandboxed(t *testing.T) {
	for _, trust := range []bool{false, true} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "page.html"), []byte("<script>1</script>"), 0o644)
		s := New(Config{Root: root, TrustHTML: trust})
		ts := httptestServer(t, s)
		resp, err := http.Get(ts + "/f/page.html")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox"); got == trust {
			t.Errorf("TrustHTML %v: CSP %q", trust, resp.Header.Get("Content-Security-Policy"))
		}
	}
}

func httptestServer(t *testing.T, s *Server) string {
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}
