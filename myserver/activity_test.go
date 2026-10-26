package myserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func activityItems(t *testing.T, res map[string]interface{}) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, it := range res["items"].([]interface{}) {
		out = append(out, it.(map[string]interface{}))
	}
	return out
}

func TestActivity(t *testing.T) {
	x := newVersionServer(t, Config{})
	x.do("POST", "/w/notes.md", "1", nil)
	x.do("POST", "/w/notes.md", "2", nil) // saves of a file are one line
	x.do("POST", "/w/notes.md", "3", nil)
	x.cmd("cp", `{"from":"/notes.md","to":"/copy.md"}`)
	x.do("POST", "/w/d/a.txt", "a", nil)
	x.do("POST", "/w/d/b.txt", "b", nil)
	x.cmd("rm", `{"paths":["/d/a.txt","/d/b.txt"]}`)
	x.do("POST", "/rm/copy.md", "", nil)
	_, h := x.cmd("history", `{"path":"/notes.md"}`)
	v := h["versions"].([]interface{})[0].(map[string]interface{})["version"].(string)
	x.cmd("restore", `{"path":"/notes.md","version":"`+v+`"}`)
	x.do("POST", "/w/.DS_Store", "junk", nil) // left out

	code, res := x.cmd("activity", "")
	if code != 200 {
		t.Fatalf("activity: %d %v", code, res)
	}
	items := activityItems(t, res)
	var got []string
	for _, it := range items {
		got = append(got, it["op"].(string)+" "+it["path"].(string))
	}
	// newest first; the two deletes in /d are one line, and so are its saves
	want := []string{"restore /notes.md", "delete /copy.md", "delete /d", "save /d", "copy /notes.md", "save /notes.md"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if items[2]["count"].(float64) != 2 || len(items[2]["names"].([]interface{})) != 2 {
		t.Fatalf("grouped deletes: %v", items[2])
	}
	if items[5]["count"].(float64) != 3 || items[4]["to"] != "/copy.md" {
		t.Fatalf("saves %v, copy %v", items[5], items[4])
	}
	if _, res := x.cmd("activity", `{"dir":"/d"}`); len(activityItems(t, res)) != 2 {
		t.Fatalf("in /d: %v", res)
	}
	if _, res := x.cmd("activity", `{"limit":2}`); len(activityItems(t, res)) != 2 || res["more"] != true {
		t.Fatalf("limit: %v", res)
	}
}

func TestActivityGroupsByTime(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	g := groupActivity([]Activity{
		{Time: t0, Op: "upload", Path: "/p/a.jpg", Size: 1},
		{Time: t0.Add(time.Minute), Op: "upload", Path: "/p/b.jpg", Size: 2},
		{Time: t0.Add(time.Minute), Op: "upload", Path: "/p/c.jpg", Size: 3, Who: "bob"},
		{Time: t0.Add(time.Hour), Op: "upload", Path: "/p/d.jpg", Size: 4},
	})
	if len(g) != 3 || g[0].Path != "/p/d.jpg" || g[1].Who != "bob" || g[2].Path != "/p" || g[2].Count != 2 || g[2].Size != 3 || g[2].Files != 2 {
		t.Fatalf("%+v", g)
	}
}

func TestActivityRotates(t *testing.T) {
	x := newVersionServer(t, Config{})
	defer func(n int64) { activityMax = n }(activityMax)
	activityMax = 300
	for i := 0; i < 20; i++ {
		x.do("POST", "/w/f"+strings.Repeat("x", i)+".txt", "1", nil)
	}
	cur, old := x.s.activityFiles()
	if fi, err := os.Stat(cur); err != nil || fi.Size() > 600 {
		t.Fatalf("not rotated: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("no older log kept")
	}
	if n := len(x.s.readActivity()); n < 5 || n >= 20 {
		t.Fatalf("read back %d", n)
	}
}

func TestActivityAccess(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, Access: newTestAccess()})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	do := func(who, method, p, body string) map[string]interface{} {
		req, _ := http.NewRequest(method, ts.URL+p, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-"+who)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var j map[string]interface{}
		json.NewDecoder(r.Body).Decode(&j)
		return j
	}
	do("alice", "POST", "/w/users/alice/a.txt", "a")
	do("bob", "POST", "/w/users/bob/b.txt", "b")
	do("alice", "POST", "/w/projects/x/p.txt", "p")
	items := func(who string) []string {
		var out []string
		for _, it := range do(who, "POST", "/cmd/activity", "")["result"].(map[string]interface{})["items"].([]interface{}) {
			m := it.(map[string]interface{})
			out = append(out, m["who"].(string)+":"+m["path"].(string))
		}
		return out
	}
	if got := strings.Join(items("bob"), " "); got != "alice:/projects/x/p.txt bob:/users/bob/b.txt" {
		t.Fatalf("bob sees: %s", got)
	}
	if got := items("root"); len(got) != 3 {
		t.Fatalf("root sees: %v", got)
	}
}
