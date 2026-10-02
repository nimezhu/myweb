package myserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirWith(t *testing.T, index string) http.FileSystem {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "index.html"), []byte(index), 0o644)
	return http.Dir(d)
}

func body(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestManagerRouting(t *testing.T) {
	cases := []struct {
		name          string
		web, manager  bool
		root, mgr     string // expected body, or "404"
		atRoot, serve bool
	}{
		{"manager only", false, true, "MANAGER", "MANAGER", true, true},
		{"app and manager", true, true, "APP", "MANAGER", false, true},
		{"app, no manager", true, false, "APP", "404", false, true},
		{"nothing", false, false, "404", "404", false, false},
	}
	for _, c := range cases {
		cfg := Config{Root: t.TempDir()}
		if c.web {
			cfg.WebFS = dirWith(t, "APP")
		}
		if c.manager {
			cfg.Manager = dirWith(t, "MANAGER")
		}
		s := New(cfg)
		ts := httptest.NewServer(s.handler())
		for path, want := range map[string]string{"/": c.root, "/_myweb/": c.mgr} {
			code, b := body(t, http.DefaultClient, ts.URL+path)
			if (want == "404" && code != 404) || (want != "404" && b != want) {
				t.Errorf("%s: GET %s = %d %q, want %s", c.name, path, code, b, want)
			}
		}
		if s.ManagerAtRoot() != c.atRoot || s.ServesRoot() != c.serve || (s.ManagerURL() != "") != c.manager {
			t.Errorf("%s: atRoot=%v serves=%v url=%q", c.name, s.ManagerAtRoot(), s.ServesRoot(), s.ManagerURL())
		}
		ts.Close()
	}
}

func TestManagerRedirectAndLogin(t *testing.T) {
	s := New(Config{Root: t.TempDir(), WebFS: dirWith(t, "APP"), Manager: dirWith(t, "MANAGER"), Token: "tok"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := noFollow.Get(ts.URL + "/_myweb")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/_myweb/" {
		t.Fatalf("/_myweb: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = noFollow.Get(ts.URL + "/_myweb/?token=tok")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/_myweb/" || len(resp.Cookies()) == 0 {
		t.Fatalf("manager login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.HasSuffix(s.ManagerLoginURL(), "/_myweb/?token=tok") {
		t.Fatalf("ManagerLoginURL: %s", s.ManagerLoginURL())
	}
}
