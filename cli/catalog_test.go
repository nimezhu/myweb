package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

func TestCatalogCmdOrigin(t *testing.T) {
	dirs := home.Dirs{Root: t.TempDir()}
	srv := myserver.New(myserver.Config{Root: t.TempDir()})
	registerCatalogCmd(srv, Spec{Name: "myweb"}, dirs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post := func(origin, body string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/cmd/catalog", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain") // a "simple" request: no preflight
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := post("https://evil.example", `{"open":"x"}`); st != 403 {
		t.Errorf("open from another site: %d", st)
	}
	if st := post("https://evil.example", `{"install":"x"}`); st != 403 {
		t.Errorf("install from another site: %d", st)
	}
	if st := post(ts.URL, `{"open":"x"}`); st == 403 {
		t.Errorf("open from its own page refused")
	}
}
