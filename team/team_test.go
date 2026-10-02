package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

func setup(t *testing.T) (*Access, map[string]string, string) {
	dir := t.TempDir()
	file := filepath.Join(dir, "team.json")
	c := &Config{}
	tokens := map[string]string{}
	for _, u := range []struct{ name, role string }{{"root", Admin}, {"alice", Member}, {"bob", Member}, {"vic", Viewer}} {
		tok, err := c.AddUser(u.name, u.role)
		if err != nil {
			t.Fatal(err)
		}
		tokens[u.name] = tok
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.Share("/projects", []string{Everyone}, nil))
	must(c.Share("/projects/x", []string{"bob", "vic"}, []string{"alice"}))
	must(c.Share("/projects/x/private", nil, []string{"alice"}))
	must(c.Save(file))
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o755)
	a, err := Open(file, data)
	if err != nil {
		t.Fatal(err)
	}
	return a, tokens, file
}

func TestPolicy(t *testing.T) {
	a, tok, _ := setup(t)
	user := func(n string) *myserver.User { return a.Identify(nil, tok[n]) }
	R, W, A := myserver.ActRead, myserver.ActWrite, myserver.ActAdmin
	cases := []struct {
		who  string
		act  myserver.Action
		p    string
		want bool
	}{
		{"alice", W, "/users/alice/a.txt", true},
		{"alice", R, "/users/bob/b.txt", false},
		{"vic", R, "/users/vic/v.txt", true},
		{"vic", W, "/users/vic/v.txt", false},   // viewers never write
		{"bob", R, "/projects/readme.md", true}, // * reads /projects
		{"bob", W, "/projects/readme.md", false},
		{"alice", W, "/projects/x/plan.md", true},
		{"bob", R, "/projects/x/plan.md", true},
		{"bob", W, "/projects/x/plan.md", false},
		{"vic", W, "/projects/x/plan.md", false},
		{"bob", R, "/projects/x/private/p.md", false},  // the most specific rule decides
		{"alice", R, "/projects/x/private/p.md", true}, // writers can read
		{"alice", R, "/top.txt", false},
		{"alice", A, "/", false},
		{"root", W, "/anything/at/all", true},
		{"root", A, "/", true},
	}
	for _, c := range cases {
		if got := a.Can(user(c.who), c.act, c.p); got != c.want {
			t.Errorf("%s %v %s = %v, want %v", c.who, c.act, c.p, got, c.want)
		}
	}
	trav := []struct {
		who, dir string
		want     bool
	}{
		{"alice", "/", true}, {"alice", "/users", true}, {"alice", "/users/bob", false},
		{"bob", "/projects", true}, {"vic", "/projects/x/private", false},
	}
	for _, c := range trav {
		if got := a.CanTraverse(user(c.who), c.dir); got != c.want {
			t.Errorf("%s traverse %s = %v, want %v", c.who, c.dir, got, c.want)
		}
	}
	if u := user("alice"); u == nil || u.Home != "/users/alice" || u.Role != Member {
		t.Fatalf("identify: %+v", u)
	}
	if a.Identify(nil, "nope") != nil || a.Identify(nil, "") != nil {
		t.Fatal("unknown token")
	}
	if _, err := os.Stat(filepath.Join(a.root, "users", "bob")); err != nil {
		t.Fatal("user folders are created")
	}
}

func TestReloadAndRevoke(t *testing.T) {
	a, tok, file := setup(t)
	alice := a.Identify(nil, tok["alice"])
	c, _ := Load(file)
	if err := c.RemoveUser("alice"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	c.Save(file)
	a.mu.Lock()
	a.checked = time.Time{} // don't wait for the once-a-second check
	a.mu.Unlock()
	if a.Identify(nil, tok["alice"]) != nil {
		t.Fatal("removed user can still sign in")
	}
	if a.Can(alice, myserver.ActRead, "/users/alice/a.txt") {
		t.Fatal("an already-identified removed user must lose access too")
	}
	if strings.Contains(strings.Join(func() []string {
		cc, _ := Load(file)
		var all []string
		for _, f := range cc.Folders {
			all = append(all, f.Read...)
			all = append(all, f.Write...)
		}
		return all
	}(), ","), "alice") {
		t.Fatal("removed user still listed in folder rules")
	}
	// a broken file keeps the previous users instead of locking everyone out
	os.WriteFile(file, []byte("{broken"), 0o600)
	a.mu.Lock()
	a.checked = time.Time{}
	a.mu.Unlock()
	if a.Identify(nil, tok["bob"]) == nil {
		t.Fatal("a broken team.json locked bob out")
	}
}

func TestConfigEdits(t *testing.T) {
	c := &Config{}
	if _, err := c.AddUser("Bad Name", Member); err == nil {
		t.Error("invalid name accepted")
	}
	c = &Config{}
	c.AddUser("ann", Member)
	if _, err := c.AddUser("ann", Member); err == nil {
		t.Error("duplicate accepted")
	}
	if err := c.SetRole("ann", "boss"); err == nil {
		t.Error("bad role accepted")
	}
	if err := c.Share("/x", []string{"ghost"}, nil); err == nil {
		t.Error("unknown user in a rule accepted")
	}
	if err := c.Share("/.myweb/history", []string{"ann"}, nil); err == nil {
		t.Error("sharing .myweb accepted")
	}
	old := c.Users[0].TokenHash
	tok, _ := c.ResetToken("ann")
	if c.Users[0].TokenHash == old || hashToken(tok) != c.Users[0].TokenHash {
		t.Error("reset")
	}
	if c.Users[0].Role != Member || len(c.Folders) != 0 {
		t.Fatalf("rejected edits must change nothing: %+v", c)
	}
	file := filepath.Join(t.TempDir(), "team.json")
	if err := c.Save(file); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Errorf("team.json permissions %v", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), tok) {
		t.Error("token stored in clear")
	}
}
