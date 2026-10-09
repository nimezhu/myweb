package myserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newProxyTestServer lets the proxy reach loopback, where the test upstream runs.
func newProxyTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	cfg.Root = t.TempDir()
	s := New(cfg)
	s.proxyClient = newProxyClient(nil)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return ts
}

func proxyURL(base, target string) string {
	return base + "/proxy?url=" + url.QueryEscape(target)
}

func TestProxy(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/data.txt", http.StatusFound)
			return
		}
		w.Header().Set("Set-Cookie", "upstream=1")
		w.Header().Set("Access-Control-Allow-Origin", "https://upstream.example")
		http.ServeContent(w, r, "data.txt", time.Time{}, strings.NewReader("0123456789"))
	}))
	defer up.Close()
	ts := newProxyTestServer(t, Config{CORS: []string{"https://app.example"}, Token: tok})

	req, _ := http.NewRequest("GET", proxyURL(ts.URL, up.URL+"/moved"), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Cookie", "myweb_token_1=secret")
	req.Header.Set("Range", "bytes=2-4")
	req.Header.Set("Origin", "https://app.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent || string(body) != "234" {
		t.Fatalf("range through redirect: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Range") != "bytes 2-4/10" {
		t.Errorf("Content-Range: %q", resp.Header.Get("Content-Range"))
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" {
		t.Errorf("credentials leaked upstream: %v", got)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("upstream cookie passed back")
	}
	if o := resp.Header.Get("Access-Control-Allow-Origin"); o != "https://app.example" {
		t.Errorf("CORS origin: %q", o)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "Content-Range") {
		t.Errorf("Content-Range not exposed: %q", resp.Header.Get("Access-Control-Expose-Headers"))
	}
	if resp.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Error("proxied content must be sandboxed")
	}
	if resp.Header.Get("X-Proxy-Final-URL") != up.URL+"/data.txt" {
		t.Errorf("final URL: %q", resp.Header.Get("X-Proxy-Final-URL"))
	}

	// a browser asks before a cross-origin Range request
	pre, _ := http.NewRequest("OPTIONS", proxyURL(ts.URL, up.URL), nil)
	pre.Header.Set("Origin", "https://app.example")
	pre.Header.Set("Access-Control-Request-Method", "GET")
	pre.Header.Set("Access-Control-Request-Headers", "range,authorization")
	resp, err = http.DefaultClient.Do(pre)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("preflight for Range refused: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestProxyRejects(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	_, ts := newAuthServer(t, false) // the real client: public addresses only
	cases := []struct {
		name, method, url string
		hdr               map[string]string
		want              int
	}{
		{"no token", "GET", proxyURL(ts.URL, "https://example.com/"), nil, 401},
		{"not http", "GET", proxyURL(ts.URL, "file:///etc/passwd"), bearer, 400},
		{"no host", "GET", proxyURL(ts.URL, "http:///x"), bearer, 400},
		{"post", "POST", proxyURL(ts.URL, "https://example.com/"), bearer, 405},
		{"loopback", "GET", proxyURL(ts.URL, up.URL), bearer, 403},
		{"localhost", "GET", proxyURL(ts.URL, strings.Replace(up.URL, "127.0.0.1", "localhost", 1)), bearer, 403},
		{"metadata", "GET", proxyURL(ts.URL, "http://169.254.169.254/latest/meta-data/"), bearer, 403},
		{"private", "GET", proxyURL(ts.URL, "http://10.0.0.1/"), bearer, 403},
		{"mapped", "GET", proxyURL(ts.URL, "http://[::ffff:127.0.0.1]/"), bearer, 403},
	}
	for _, c := range cases {
		if r, _ := do(t, c.method, c.url, "", c.hdr); r.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, r.StatusCode, c.want)
		}
	}
}

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"93.184.216.34:443":       true,
		"[2606:2800:220:1::]:443": true,
		"127.0.0.1:80":            false,
		"[::1]:80":                false,
		"10.1.2.3:80":             false,
		"172.16.0.1:80":           false,
		"192.168.1.1:80":          false,
		"169.254.169.254:80":      false,
		"100.100.100.100:80":      false,
		"0.0.0.0:80":              false,
		"[fe80::1]:80":            false,
		"[fd00::1]:80":            false,
		"[::ffff:10.0.0.1]:80":    false,
		"224.0.0.1:80":            false,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("%s: err=%v, want allowed=%v", addr, err, ok)
		}
	}
}
