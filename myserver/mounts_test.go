package myserver

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// newMountTest starts a real server (mounts listen on ports of their own)
// with the file manager at "/" and two mounted apps.
func newMountTest(t *testing.T) (*Server, int, int) {
	root := t.TempDir()
	for p, body := range map[string]string{"a/x.txt": "a's", "b/y.txt": "b's", "secret.txt": "secret"} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	s := New(Config{Root: root, Port: freePort(t), CORS: DefaultCORS,
		Manager: http.FS(fstest.MapFS{"index.html": {Data: []byte("the manager")}})})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown(t.Context()) })
	mount := func(name string) int {
		p, err := s.Mount(&Mount{
			Access:  &AppAccess{Name: name, Folders: func() []Folder { return []Folder{{Path: "/" + name}} }},
			FS:      http.FS(fstest.MapFS{"index.html": {Data: []byte("app " + name)}}),
			Version: func() string { return "1.0." + name },
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	return s, mount("a"), mount("b")
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// get asks port as a page of origin would ("" for a program).
func mget(t *testing.T, method string, port int, p, origin string) (int, string, http.Header) {
	t.Helper()
	u := "http://127.0.0.1:" + strconv.Itoa(port) + p
	req, _ := http.NewRequest(method, u, strings.NewReader("data"))
	if origin != "" {
		if method == "GET" && origin == "http://127.0.0.1:"+strconv.Itoa(port) {
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		} else {
			req.Header.Set("Origin", origin)
		}
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

func TestMounts(t *testing.T) {
	s, pa, pb := newMountTest(t)
	main := s.Port()
	if pa == pb || pa == main || pb == main {
		t.Fatalf("ports %d %d %d", main, pa, pb)
	}
	aOrigin := "http://127.0.0.1:" + strconv.Itoa(pa)

	// each app's pages on its port, with its version
	st, body, h := mget(t, "GET", pa, "/", aOrigin)
	if st != 200 || body != "app a" || !strings.Contains(h.Get("Server-Timing"), "1.0.a") {
		t.Fatalf("app a: %d %q %v", st, body, h)
	}
	if _, body, _ := mget(t, "GET", pb, "/", ""); body != "app b" {
		t.Fatalf("app b: %q", body)
	}
	// its folders only, on its port and on any other
	for _, port := range []int{pa, main, pb} {
		if st, _, _ := mget(t, "GET", port, "/f/a/x.txt", aOrigin); st != 200 {
			t.Errorf("a reads its file via %d: %d", port, st)
		}
		if st, _, _ := mget(t, "GET", port, "/f/b/y.txt", aOrigin); st != 404 {
			t.Errorf("a reads b's file via %d: %d", port, st)
		}
		if st, _, _ := mget(t, "GET", port, "/f/secret.txt", aOrigin); st != 404 {
			t.Errorf("a reads a secret via %d: %d", port, st)
		}
	}
	if st, _, _ := mget(t, "POST", pa, "/w/a/new.txt", aOrigin); st != 200 {
		t.Errorf("a writes its folder: %d", st)
	}
	if st, _, _ := mget(t, "POST", pa, "/w/b/new.txt", aOrigin); st != 403 {
		t.Errorf("a writes b's folder: %d", st)
	}
	// the file manager (at "/" of the main port, no app there) has everything
	mainOrigin := "http://127.0.0.1:" + strconv.Itoa(main)
	if st, body, _ := mget(t, "GET", main, "/", mainOrigin); st != 200 || body != "the manager" {
		t.Errorf("manager: %d %q", st, body)
	}
	if st, _, _ := mget(t, "GET", main, "/f/secret.txt", mainOrigin); st != 200 {
		t.Errorf("manager reads: %d", st)
	}
	// ... and isn't served on an app's port
	st, _, h = mget(t, "GET", pa, "/_myweb/", "")
	if st != http.StatusTemporaryRedirect || h.Get("Location") != mainOrigin+"/_myweb/" {
		t.Errorf("manager on a's port: %d %s", st, h.Get("Location"))
	}
	// /apps/NAME/ on the main port leads to the app
	st, _, h = mget(t, "GET", main, "/apps/b/page?x=1", "")
	if st != http.StatusTemporaryRedirect || h.Get("Location") != "http://127.0.0.1:"+strconv.Itoa(pb)+"/page?x=1" {
		t.Errorf("/apps/b/: %d %s", st, h.Get("Location"))
	}
	if st, _, _ := mget(t, "GET", main, "/apps/nope/", ""); st == http.StatusTemporaryRedirect {
		t.Error("/apps/nope/ redirected")
	}
	if ms := s.Mounted(); len(ms) != 2 || ms[0].Name != "a" || ms[0].Port != pa {
		t.Errorf("Mounted: %v", ms)
	}
	// mounting a name again keeps its port; a busy port is replaced by a free one
	if p, _ := s.Mount(&Mount{Access: &AppAccess{Name: "a", Folders: func() []Folder { return nil }}, FS: http.FS(fstest.MapFS{})}, 0); p != pa {
		t.Errorf("mounted a again on %d", p)
	}
	p, err := s.Mount(&Mount{Access: &AppAccess{Name: "c", Folders: func() []Folder { return nil }}, FS: http.FS(fstest.MapFS{})}, pa)
	if err != nil || p == pa || p == main || p == pb {
		t.Errorf("c on a's port: %d %v", p, err)
	}
	s.Unmount("b")
	if _, err := http.Get("http://127.0.0.1:" + strconv.Itoa(pb) + "/"); err == nil {
		t.Error("b still served after Unmount")
	}
	if u := s.MountURL("a"); u != aOrigin+"/" {
		t.Errorf("MountURL = %s", u)
	}
}
