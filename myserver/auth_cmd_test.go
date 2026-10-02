package myserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tok = "secret-token"

func newAuthServer(t *testing.T, global bool) (*Server, *httptest.Server) {
	t.Helper()
	s := New(Config{Root: t.TempDir(), Token: tok, Global: global})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, map[string]interface{}) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&j)
	return resp, j
}

var bearer = map[string]string{"Authorization": "Bearer " + tok}

func TestAuthRequiredForWrites(t *testing.T) {
	_, ts := newAuthServer(t, false)
	if r, _ := do(t, "POST", ts.URL+"/w/a.txt", "x", nil); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", ts.URL+"/w/a.txt", "x", map[string]string{"Authorization": "Bearer nope"}); r.StatusCode != 401 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", ts.URL+"/w/a.txt", "x", bearer); r.StatusCode != 200 {
		t.Fatalf("good token: %d", r.StatusCode)
	}
	// local reads stay open
	if r, _ := do(t, "GET", ts.URL+"/ls/", "", nil); r.StatusCode != 200 {
		t.Fatalf("local ls: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", ts.URL+"/cmd/fetchUrl?url=http://x", "", nil); r.StatusCode != 401 {
		t.Fatalf("fetchUrl without token: %d", r.StatusCode)
	}
}

func TestGlobalProtectsReads(t *testing.T) {
	_, ts := newAuthServer(t, true)
	if r, _ := do(t, "GET", ts.URL+"/ls/", "", nil); r.StatusCode != 401 {
		t.Fatalf("global ls without token: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", ts.URL+"/ls/", "", bearer); r.StatusCode != 200 {
		t.Fatalf("global ls with token: %d", r.StatusCode)
	}
}

func TestLoginCookie(t *testing.T) {
	s, ts := newAuthServer(t, false)
	r, _ := do(t, "GET", ts.URL+"/?token="+tok+"&x=1", "", nil)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/?x=1" {
		t.Fatalf("login redirect: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == s.cookieName() {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("login cookie not set or not HttpOnly")
	}
	if r, _ := do(t, "POST", ts.URL+"/w/b.txt", "x", map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}); r.StatusCode != 200 {
		t.Fatalf("write with cookie: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", ts.URL+"/?token=wrong", "", nil); r.StatusCode != 401 {
		t.Fatalf("bad login token: %d", r.StatusCode)
	}
}

func TestCmdRegistry(t *testing.T) {
	s, ts := newAuthServer(t, false)
	s.RegisterCmd("echo", func(ctx context.Context, r *CmdRequest) (interface{}, error) {
		var p struct{ Msg string }
		if err := r.Bind(&p); err != nil {
			return nil, err
		}
		if p.Msg == "" {
			return nil, Errorf(http.StatusBadRequest, "msg required")
		}
		return p.Msg, nil
	}, Describe("echo a message"))
	s.RegisterCmd("ping", func(context.Context, *CmdRequest) (interface{}, error) { return "pong", nil }, Public())
	s.RegisterCmd("touch", func(context.Context, *CmdRequest) (interface{}, error) { return "ok", nil }, Writes())
	s.RegisterCmd("boom", func(context.Context, *CmdRequest) (interface{}, error) { panic("boom") })

	if r, j := do(t, "POST", ts.URL+"/cmd/echo", `{"msg":"hi"}`, bearer); r.StatusCode != 200 || j["result"] != "hi" {
		t.Fatalf("echo: %d %v", r.StatusCode, j)
	}
	if r, _ := do(t, "POST", ts.URL+"/cmd/echo", `{"msg":"hi"}`, nil); r.StatusCode != 401 {
		t.Fatalf("echo without token: %d", r.StatusCode)
	}
	if r, j := do(t, "POST", ts.URL+"/cmd/echo", `{}`, bearer); r.StatusCode != 400 || j["error"] != "msg required" {
		t.Fatalf("echo error: %d %v", r.StatusCode, j)
	}
	if r, _ := do(t, "POST", ts.URL+"/cmd/echo", `not json`, bearer); r.StatusCode != 400 {
		t.Fatalf("bad json: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", ts.URL+"/cmd/echo", "", bearer); r.StatusCode != 405 {
		t.Fatalf("GET cmd: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", ts.URL+"/cmd/nope", "", bearer); r.StatusCode != 404 {
		t.Fatalf("unknown cmd: %d", r.StatusCode)
	}
	if r, j := do(t, "POST", ts.URL+"/cmd/ping", "", nil); r.StatusCode != 200 || j["result"] != "pong" {
		t.Fatalf("public cmd: %d", r.StatusCode)
	}
	s.SetReadOnly(true)
	if r, _ := do(t, "POST", ts.URL+"/cmd/touch", "", bearer); r.StatusCode != 403 {
		t.Fatalf("writes cmd in read-only: %d", r.StatusCode)
	}
	s.SetReadOnly(false)
	if r, _ := do(t, "POST", ts.URL+"/cmd/boom", "", bearer); r.StatusCode != 500 {
		t.Fatalf("panicking cmd: %d", r.StatusCode)
	}
	r, j := do(t, "GET", ts.URL+"/cmd/", "", bearer)
	names := map[string]bool{}
	list, _ := j["result"].([]interface{})
	for _, c := range list {
		names[c.(map[string]interface{})["name"].(string)] = true
	}
	for _, n := range []string{"echo", "ping", "touch", "boom", "history", "restore", "trash", "untrash", "emptyTrash"} {
		if r.StatusCode != 200 || !names[n] {
			t.Fatalf("list misses %s: %d %v", n, r.StatusCode, j)
		}
	}
	// built-in routes still win over the registry
	if r, _ := do(t, "GET", ts.URL+"/cmd/status", "", nil); r.StatusCode != 200 {
		t.Fatalf("status: %d", r.StatusCode)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration should panic")
		}
	}()
	s.RegisterCmd("ping", nil)
}
