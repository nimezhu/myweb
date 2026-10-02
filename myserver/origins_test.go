package myserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseOrigins(t *testing.T) {
	got, err := ParseOrigins(" https://app.example.com/, https://*.example.org ,http://localhost:*,null,")
	if err != nil || len(got) != 4 || got[0] != "https://app.example.com" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"*", "app.example.com", "ftp://x.com", "https://x.com/path", "https://"} {
		if _, err := ParseOrigins(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	var l OriginList
	l.Set("https://a.com")
	l.Set("https://b.com,https://c.com")
	if len(l) != 3 || l.String() != "https://a.com,https://b.com,https://c.com" {
		t.Fatalf("list: %v", l)
	}
}

func TestExtraOriginAndExposedETag(t *testing.T) {
	s := New(Config{Root: t.TempDir(), CORS: []string{"https://app.example.com"}})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/version", nil)
	req.Header.Set("Origin", "https://app.example.com")
	resp, _ := http.DefaultClient.Do(req)
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || resp.Header.Get("Access-Control-Expose-Headers") != "Etag" {
		t.Fatalf("cors headers: %v", resp.Header)
	}
	// preflight for a conditional write
	req, _ = http.NewRequest("OPTIONS", ts.URL+"/w/a.txt", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "if-match, authorization")
	resp, _ = http.DefaultClient.Do(req)
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("preflight with If-Match rejected")
	}
}

func TestDefaultCORSValid(t *testing.T) {
	if _, err := ParseOrigins(strings.Join(DefaultCORS, ",")); err != nil {
		t.Fatal(err)
	}
}
