package myserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testAccess: alice and bob are members with private folders, vic a viewer,
// root an admin; /projects/x is shared (alice writes, bob and vic read);
// /secret is nobody's.
type testAccess struct{ users map[string]*User }

func newTestAccess() *testAccess {
	return &testAccess{map[string]*User{
		"tok-alice": {Name: "alice", Role: "member", Home: "/users/alice"},
		"tok-bob":   {Name: "bob", Role: "member", Home: "/users/bob"},
		"tok-vic":   {Name: "vic", Role: "viewer", Home: "/users/vic"},
		"tok-root":  {Name: "root", Role: "admin"},
	}}
}

func (a *testAccess) Identify(r *http.Request, token string) *User { return a.users[token] }

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func (a *testAccess) Can(u *User, act Action, p string) bool {
	switch {
	case u.Role == "admin":
		return true
	case act == ActAdmin:
		return false
	case u.Home != "" && under(p, u.Home):
		return act == ActRead || u.Role == "member"
	case under(p, "/projects/x"):
		if act == ActRead {
			return u.Name == "bob" || u.Name == "vic" || u.Name == "alice"
		}
		return u.Name == "alice"
	}
	return false
}

func (a *testAccess) CanTraverse(u *User, dir string) bool {
	for _, d := range []string{u.Home, "/projects/x"} {
		if d != "" && (dir == "/" || under(d, dir)) {
			return true
		}
	}
	return u.Role == "admin"
}

type mu struct {
	t   *testing.T
	s   *Server
	url string
	dir string
}

func newMultiUser(t *testing.T) *mu {
	root := t.TempDir()
	for p, body := range map[string]string{
		"users/alice/a.txt": "alice private TP53", "users/bob/b.txt": "bob private TP53",
		"projects/x/plan.md": "shared TP53", "secret/s.txt": "secret TP53", "top.txt": "top TP53",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "users/vic"), 0o755)
	var events []RequestEvent
	s := New(Config{Root: root, Access: newTestAccess(), OnRequest: func(e RequestEvent) { events = append(events, e) }})
	registerTestPlugins(s)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return &mu{t, s, ts.URL, root}
}

// a minimal stat/find so commands go through CmdRequest.Path / Visible
func registerTestPlugins(s *Server) {
	s.RegisterCmd("stat", func(_ context.Context, req *CmdRequest) (interface{}, error) {
		var p struct{ Path string }
		req.Bind(&p)
		fp, err := req.Path(ActRead, p.Path)
		if err != nil {
			return nil, err
		}
		_, err = os.Stat(fp)
		return map[string]bool{"exists": err == nil}, nil
	})
}

func (m *mu) do(who, method, p string, body io.Reader, ctype string) (int, string) {
	m.t.Helper()
	req, _ := http.NewRequest(method, m.url+p, body)
	if who != "" {
		req.Header.Set("Authorization", "Bearer tok-"+who)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (m *mu) cmd(who, name, params string) (int, map[string]interface{}) {
	code, body := m.do(who, "POST", "/cmd/"+name, strings.NewReader(params), "application/json")
	var j map[string]interface{}
	json.Unmarshal([]byte(body), &j)
	return code, j
}

func names(body string) string {
	var list []struct{ Name string }
	json.Unmarshal([]byte(body), &list)
	var out []string
	for _, e := range list {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestMultiUserReads(t *testing.T) {
	m := newMultiUser(t)
	cases := []struct {
		who, path string
		want      int
	}{
		{"", "/f/users/alice/a.txt", 401},
		{"alice", "/f/users/alice/a.txt", 200},
		{"alice", "/f/users/bob/b.txt", 404}, // not 403: don't reveal it exists
		{"bob", "/f/projects/x/plan.md", 200},
		{"vic", "/f/projects/x/plan.md", 200},
		{"alice", "/f/secret/s.txt", 404},
		{"alice", "/f/top.txt", 404},
		{"root", "/f/secret/s.txt", 200},
		{"alice", "/f/users/bob/", 404},
		{"alice", "/ls/users/bob", 404},
		{"alice", "/ls/secret", 404},
	}
	for _, c := range cases {
		if code, _ := m.do(c.who, "GET", c.path, nil, ""); code != c.want {
			t.Errorf("%s GET %s = %d, want %d", c.who, c.path, code, c.want)
		}
	}
	// listings show only what each user may see
	for _, c := range []struct{ who, dir, want string }{
		{"alice", "/ls/", "projects,users"},
		{"alice", "/ls/users", "alice"},
		{"bob", "/ls/users", "bob"},
		{"vic", "/ls/projects", "x"},
		{"root", "/ls/", "projects,secret,top.txt,users"},
	} {
		if _, body := m.do(c.who, "GET", c.dir, nil, ""); names(body) != c.want {
			t.Errorf("%s %s = %q, want %q", c.who, c.dir, names(body), c.want)
		}
	}
	// the /f/ folder page is filtered too, and no ETag leaks for hidden files
	if _, body := m.do("alice", "GET", "/f/users/", nil, ""); strings.Contains(body, "bob") || !strings.Contains(body, "alice") {
		t.Errorf("/f/users/ listing for alice: %s", body)
	}
	req, _ := http.NewRequest("GET", m.url+"/f/users/bob/b.txt", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	if resp, _ := http.DefaultClient.Do(req); resp.Header.Get("ETag") != "" {
		t.Error("ETag sent for a hidden file")
	}
}

func TestMultiUserWrites(t *testing.T) {
	m := newMultiUser(t)
	cases := []struct {
		who, path string
		want      int
	}{
		{"alice", "/w/users/alice/new.txt", 200},
		{"alice", "/w/users/bob/x.txt", 403},
		{"bob", "/w/users/alice/x.txt", 403},
		{"alice", "/w/projects/x/plan.md", 200},
		{"bob", "/w/projects/x/plan.md", 403},
		{"vic", "/w/users/vic/mine.txt", 403}, // viewers never write
		{"alice", "/w/top.txt", 403},
		{"root", "/w/secret/new.txt", 200},
		{"", "/w/users/alice/x.txt", 401},
		{"bob", "/rm/users/alice/a.txt", 403},
	}
	for _, c := range cases {
		if code, body := m.do(c.who, "POST", c.path, strings.NewReader("data"), ""); code != c.want {
			t.Errorf("%s POST %s = %d %s, want %d", c.who, c.path, code, body, c.want)
		}
	}
	// uploads: per file, and editor uploads land in the user's own folder
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("f", "up.txt")
	fw.Write([]byte("u"))
	mw.Close()
	if code, _ := m.do("bob", "POST", "/up/users/alice", bytes.NewReader(buf.Bytes()), mw.FormDataContentType()); code != 403 {
		t.Errorf("bob upload into alice: %d", code)
	}
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("myFile", "pic.png")
	fw.Write([]byte("png"))
	mw.Close()
	code, body := m.do("alice", "POST", "/cmd/uploadFile", &buf, mw.FormDataContentType())
	if code != 200 || !strings.Contains(body, `"url":"/f/users/alice/uploads/`) {
		t.Errorf("editor upload: %d %s", code, body)
	}
}

func TestMultiUserCommandsHistoryTrash(t *testing.T) {
	m := newMultiUser(t)
	if code, _ := m.cmd("alice", "stat", `{"path":"/users/bob/b.txt"}`); code != 403 {
		t.Errorf("stat other's file: %d", code)
	}
	if code, _ := m.cmd("alice", "stat", `{"path":"/users/alice/a.txt"}`); code != 200 {
		t.Errorf("stat own file: %d", code)
	}
	// history and restore follow the file's permissions
	m.do("alice", "POST", "/w/users/alice/a.txt", strings.NewReader("v2"), "")
	if code, _ := m.cmd("bob", "history", `{"path":"/users/alice/a.txt"}`); code != 403 {
		t.Errorf("bob history of alice's file: %d", code)
	}
	code, j := m.cmd("alice", "history", `{"path":"/users/alice/a.txt"}`)
	vs := j["result"].(map[string]interface{})["versions"].([]interface{})
	if code != 200 || len(vs) != 1 {
		t.Fatalf("alice history: %d %v", code, j)
	}
	vurl := vs[0].(map[string]interface{})["url"].(string)
	if code, _ := m.do("alice", "GET", vurl, nil, ""); code != 200 {
		t.Errorf("alice reads her version: %d", code)
	}
	if code, _ := m.do("bob", "GET", vurl, nil, ""); code != 404 {
		t.Errorf("bob reads alice's version: %d", code)
	}
	// trash: who deleted it; others don't see it
	m.do("alice", "POST", "/rm/users/alice/a.txt", nil, "")
	m.do("bob", "POST", "/rm/users/bob/b.txt", nil, "")
	_, j = m.cmd("alice", "trash", "")
	items := j["result"].(map[string]interface{})["items"].([]interface{})
	if len(items) != 1 || items[0].(map[string]interface{})["path"] != "/users/alice/a.txt" || items[0].(map[string]interface{})["by"] != "alice" {
		t.Fatalf("alice's trash: %v", items)
	}
	turl := items[0].(map[string]interface{})["url"].(string)
	if code, _ := m.do("bob", "GET", turl, nil, ""); code != 404 {
		t.Errorf("bob reads alice's trashed file: %d", code)
	}
	id := items[0].(map[string]interface{})["id"].(string)
	if code, _ := m.cmd("bob", "untrash", `{"id":"`+id+`"}`); code != 404 {
		t.Errorf("bob untrashes alice's file: %d", code)
	}
	if code, _ := m.cmd("alice", "emptyTrash", ""); code != 403 {
		t.Errorf("member empties the trash: %d", code)
	}
	_, j = m.cmd("root", "trash", "")
	if n := len(j["result"].(map[string]interface{})["items"].([]interface{})); n != 2 {
		t.Errorf("admin sees all trash: %d", n)
	}
	if code, _ := m.cmd("alice", "untrash", `{"id":"`+id+`"}`); code != 200 {
		t.Errorf("alice puts hers back: %d", code)
	}
	if code, _ := m.do("alice", "POST", "/cmd/stopServer", nil, ""); code != 403 {
		t.Errorf("member stops the server: %d", code)
	}
	// status says who you are
	_, body := m.do("bob", "GET", "/cmd/status", nil, "")
	if !strings.Contains(body, `"name":"bob"`) {
		t.Errorf("status: %s", body)
	}
}

func TestMultiUserEvents(t *testing.T) {
	m := newMultiUser(t)
	dial := func(who string) *websocket.Conn {
		h := http.Header{"Authorization": {"Bearer tok-" + who}}
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(m.url, "http")+"/events", h)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		var hello map[string]interface{}
		c.ReadJSON(&hello)
		return c
	}
	alice := dial("alice")
	alice.WriteJSON(map[string]interface{}{"op": "watch", "path": "/", "recursive": true})
	var ack map[string]interface{}
	alice.SetReadDeadline(time.Now().Add(3 * time.Second))
	if alice.ReadJSON(&ack); ack["type"] != "watching" {
		t.Fatalf("alice watch /: %v", ack)
	}
	alice.WriteJSON(map[string]interface{}{"op": "watch", "path": "/users/bob"})
	alice.ReadJSON(&ack)
	if ack["type"] != "error" {
		t.Fatalf("alice may not watch bob's folder: %v", ack)
	}
	time.Sleep(100 * time.Millisecond)
	m.do("bob", "POST", "/w/users/bob/new.txt", strings.NewReader("x"), "")
	m.do("alice", "POST", "/w/users/alice/mine.txt", strings.NewReader("x"), "")
	seen := map[string]bool{}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		alice.SetReadDeadline(deadline)
		var e map[string]interface{}
		if alice.ReadJSON(&e) != nil {
			break
		}
		if e["type"] == "change" {
			seen[e["path"].(string)] = true
		}
	}
	if !seen["/users/alice/mine.txt"] {
		t.Errorf("alice should see her own change: %v", seen)
	}
	for p := range seen {
		if strings.HasPrefix(p, "/users/bob") {
			t.Errorf("alice saw bob's change %s", p)
		}
	}
}

func TestMultiUserRequestLogAndLogin(t *testing.T) {
	var got []string
	s := New(Config{Root: t.TempDir(), Access: newTestAccess(), OnRequest: func(e RequestEvent) { got = append(got, e.User+" "+path.Base(e.Path)) }})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/cmd/status", nil)
	req.Header.Set("Authorization", "Bearer tok-bob")
	http.DefaultClient.Do(req)
	if len(got) != 1 || got[0] != "bob status" {
		t.Fatalf("request log: %v", got)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, _ := noFollow.Get(ts.URL + "/?token=tok-alice"); resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) == 0 {
		t.Fatalf("personal sign-in link: %d", resp.StatusCode)
	}
	if resp, _ := noFollow.Get(ts.URL + "/?token=nope"); resp.StatusCode != 401 {
		t.Fatalf("bad link: %d", resp.StatusCode)
	}
	if s.LoginURL() != s.URL() {
		t.Fatalf("no server-wide token in multi-user mode: %s", s.LoginURL())
	}
}
