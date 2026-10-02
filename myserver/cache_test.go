package myserver

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func get(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestFileCaching(t *testing.T) {
	_, ts, root := newTestServer(t)
	p := filepath.Join(root, "data.txt")
	os.WriteFile(p, []byte("0123456789"), 0o644)

	r := get(t, ts.URL+"/f/data.txt", nil)
	etag := r.Header.Get("ETag")
	if r.StatusCode != 200 || etag == "" || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("first GET: %d etag=%q cc=%q", r.StatusCode, etag, r.Header.Get("Cache-Control"))
	}
	if r := get(t, ts.URL+"/f/data.txt", map[string]string{"If-None-Match": etag}); r.StatusCode != 304 {
		t.Fatalf("unchanged: %d, want 304", r.StatusCode)
	}
	// Range still works, and If-Range with the current ETag honours it
	if r := get(t, ts.URL+"/f/data.txt", map[string]string{"Range": "bytes=2-4", "If-Range": etag}); r.StatusCode != 206 {
		t.Fatalf("range: %d, want 206", r.StatusCode)
	}

	// change the file (same size, new mtime): new ETag, full response
	os.WriteFile(p, []byte("abcdefghij"), 0o644)
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(p, later, later)
	r = get(t, ts.URL+"/f/data.txt", map[string]string{"If-None-Match": etag})
	if r.StatusCode != 200 || r.Header.Get("ETag") == etag {
		t.Fatalf("changed: %d etag=%q", r.StatusCode, r.Header.Get("ETag"))
	}
	// stale If-Range: whole file instead of the range
	if r := get(t, ts.URL+"/f/data.txt", map[string]string{"Range": "bytes=2-4", "If-Range": etag}); r.StatusCode != 200 {
		t.Fatalf("stale If-Range: %d, want 200", r.StatusCode)
	}
	// directories and missing files: no ETag, still no-cache
	os.Mkdir(filepath.Join(root, "dir"), 0o755)
	if r := get(t, ts.URL+"/f/dir/", nil); r.Header.Get("ETag") != "" || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("directory listing headers")
	}
	if r := get(t, ts.URL+"/f/missing", nil); r.StatusCode != 404 {
		t.Fatalf("missing: %d", r.StatusCode)
	}
}
