package console

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

func readLines(t *testing.T, p string) []map[string]interface{} {
	t.Helper()
	b, _ := os.ReadFile(p)
	var out []map[string]interface{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func TestAccessLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "access.log")
	c := New(Options{NoTUI: true})
	if err := c.OpenLog(p); err != nil {
		t.Fatal(err)
	}
	c.stderr.SetOutput(&strings.Builder{}) // keep test output quiet
	c.OnRequest(myserver.RequestEvent{Time: time.Now(), Method: "POST", Path: "/w/a", Status: 412,
		Duration: 1500 * time.Microsecond, Error: "{\"error\":\"file changed\"}\n"})
	c.logOutput(&strings.Builder{})
	defer log.SetOutput(os.Stderr)
	log.Printf("wrote /a (3 bytes)")
	log.Printf("two\nlines")
	c.alog.close()

	lines := readLines(t, p)
	if len(lines) != 4 {
		t.Fatalf("lines: %v", lines)
	}
	r := lines[0]
	if r["type"] != "request" || r["status"].(float64) != 412 || r["ms"].(float64) != 1.5 || r["error"] != `{"error":"file changed"}` {
		t.Fatalf("request line: %v", r)
	}
	if lines[1]["type"] != "log" || lines[1]["msg"] != "wrote /a (3 bytes)" || lines[3]["msg"] != "lines" {
		t.Fatalf("log lines: %v", lines[1:])
	}
	if c.opts.Rows[0].Label != "Log" {
		t.Fatal("Log row")
	}
}

func TestAccessLogRotation(t *testing.T) {
	old := logRotateSize
	logRotateSize = 300
	defer func() { logRotateSize = old }()
	p := filepath.Join(t.TempDir(), "access.log")
	a, err := openAccessLog(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		a.message(time.Now(), strings.Repeat("x", 50))
	}
	a.close()
	cur, _ := os.Stat(p)
	prev, err := os.Stat(p + ".1")
	if err != nil || cur.Size() > 300 || prev.Size() > 300 {
		t.Fatalf("rotation: cur=%v prev=%v err=%v", cur, prev, err)
	}
	if n := len(readLines(t, p)) + len(readLines(t, p+".1")); n < 4 {
		t.Fatalf("lines kept across files: %d", n)
	}
}

func TestOpenLogError(t *testing.T) {
	if err := New(Options{}).OpenLog(filepath.Join(t.TempDir(), "no", "such", "dir", "x.log")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}
