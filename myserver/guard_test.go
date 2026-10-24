package myserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrossSiteWritesRefused(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, CORS: append(append([]string{}, DefaultCORS...), "https://app.example.com")})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644)

	do := func(method, path string, hdr map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader("data"))
		req.Header.Set("Content-Type", "text/plain") // a "simple" request: no preflight
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	evil := map[string]string{"Origin": "https://evil.example"}
	for _, path := range []string{"/w/evil.txt", "/rm/keep.txt", "/up/", "/cmd/mkdir", "/cmd/stopServer"} {
		if st := do("POST", path, evil); st != http.StatusForbidden {
			t.Errorf("POST %s from another site: %d", path, st)
		}
	}
	if st := do("POST", "/w/evil.txt", map[string]string{"Origin": "null"}); st != http.StatusForbidden {
		t.Errorf("Origin null: %d", st)
	}
	if st := do("POST", "/w/evil.txt", map[string]string{"Sec-Fetch-Site": "cross-site"}); st != http.StatusForbidden {
		t.Errorf("cross-site without Origin: %d", st)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
		t.Fatal("a cross-site request wrote a file")
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatal("a cross-site request deleted a file")
	}

	// reads stay open (CORS decides who may see the answer)
	if st := do("GET", "/f/keep.txt", evil); st != 200 {
		t.Errorf("GET from another site: %d", st)
	}
	// allowed: no Origin (curl, scripts), the server's own pages, CORS origins
	for name, hdr := range map[string]map[string]string{
		"no origin":   nil,
		"same origin": {"Origin": ts.URL, "Sec-Fetch-Site": "same-origin"},
		"default":     {"Origin": "https://youdata.studio"},
		"-cors":       {"Origin": "https://app.example.com"},
		"local page":  {"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-site"},
	} {
		if st := do("POST", "/w/ok.txt", hdr); st != 200 {
			t.Errorf("%s: %d", name, st)
		}
	}
	// a CORS preflight still works
	req, _ := http.NewRequest("OPTIONS", ts.URL+"/w/ok.txt", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
}
