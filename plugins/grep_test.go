package plugins

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/myserver"
)

func TestGrep(t *testing.T) {
	root := t.TempDir()
	srv := myserver.New(myserver.Config{Root: root})
	RegisterFileOps(srv)
	ts := httptest.NewServer(srv.Mux())
	defer ts.Close()
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	put("notes/a.md", "intro\nTP53 is a tumor suppressor\nnothing here\ntp53 again\n")
	put("notes/b.txt", "TP53 in a text file\n")
	put("data/bin.dat", "TP53\x00\x01binary")
	put(".hidden/secret.md", "TP53 hidden\n")
	put("long.txt", strings.Repeat("x", 500)+"TP53"+strings.Repeat("y", 500)+"\n")

	matches := func(j map[string]interface{}) []string {
		var out []string
		for _, m := range j["matches"].([]interface{}) {
			mm := m.(map[string]interface{})
			out = append(out, mm["path"].(string)+":"+strings.TrimSuffix(strings.TrimSuffix(
				strings.Split(mm["text"].(string), " ")[0], "…"), ""))
		}
		return out
	}
	code, j := run(t, ts.URL, "grep", `{"pattern":"tp53"}`)
	res := j["result"].(map[string]interface{})
	if code != 200 || len(res["matches"].([]interface{})) != 4 || res["skipped"].(float64) != 1 {
		t.Fatalf("plain, ignore case: %d %v", code, j)
	}
	got := strings.Join(matches(res), ",")
	if strings.Contains(got, "hidden") || strings.Contains(got, "bin.dat") {
		t.Fatalf("hidden dirs and binaries must be skipped: %s", got)
	}
	first := res["matches"].([]interface{})[1].(map[string]interface{})
	if first["path"] != "/notes/a.md" || first["line"].(float64) != 2 {
		t.Fatalf("line numbers: %v", first)
	}
	// excerpt around a match in a long line
	for _, m := range res["matches"].([]interface{}) {
		mm := m.(map[string]interface{})
		if mm["path"] == "/long.txt" {
			if txt := mm["text"].(string); len(txt) > 200 || !strings.Contains(txt, "TP53") || !strings.HasPrefix(txt, "…") {
				t.Fatalf("excerpt: %q", txt)
			}
		}
	}
	_, j = run(t, ts.URL, "grep", `{"pattern":"TP53","ignoreCase":false,"dir":"/notes","glob":"*.md"}`)
	if n := len(j["result"].(map[string]interface{})["matches"].([]interface{})); n != 1 {
		t.Fatalf("case-sensitive + dir + glob: %d", n)
	}
	_, j = run(t, ts.URL, "grep", `{"pattern":"^tp53","regex":true}`)
	if n := len(j["result"].(map[string]interface{})["matches"].([]interface{})); n != 3 {
		t.Fatalf("regex: %d", n)
	}
	_, j = run(t, ts.URL, "grep", `{"pattern":"tp53","limit":2}`)
	if r := j["result"].(map[string]interface{}); len(r["matches"].([]interface{})) != 2 || r["truncated"] != true {
		t.Fatalf("limit: %v", r)
	}
	for _, bad := range []string{`{}`, `{"pattern":"(","regex":true}`, `{"pattern":"x","glob":"["}`} {
		if code, _ := run(t, ts.URL, "grep", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}
