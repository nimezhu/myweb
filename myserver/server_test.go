package myserver

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	s := New(Config{Root: root})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return s, ts, root
}

func post(t *testing.T, url, ctype string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	return resp
}

// Bad POSTs must get an error response and never take the server down.
func TestBadPostsDoNotKillServer(t *testing.T) {
	s, ts, root := newTestServer(t)
	cases := []struct {
		name, path, ctype string
		body              string
		want              int
	}{
		{"write root", "/w/", "", "x", 400},
		{"write traversal", "/w/../../etc/evil", "", "x", 0},
		{"rm missing", "/rm/nope.txt", "", "", 404},
		{"rm root", "/rm/", "", "", 400},
		{"upload not multipart", "/cmd/uploadFile", "text/plain", "garbage", 400},
		{"upload broken multipart", "/cmd/uploadFile", "multipart/form-data; boundary=zz", "--zz\r\nbroken", 400},
		{"upload missing field", "/cmd/uploadFile", "multipart/form-data; boundary=zz", "--zz--\r\n", 400},
		{"fetch bad url", "/cmd/fetchUrl?url=file:///etc/passwd", "", "", 400},
		{"stop from foreign origin", "/cmd/stopServer", "text/plain", "{}", 403},
	}
	for _, c := range cases {
		hdr := map[string]string{}
		if c.name == "stop from foreign origin" {
			hdr["Origin"] = "https://evil.example"
		}
		resp := post(t, ts.URL+c.path, c.ctype, strings.NewReader(c.body), hdr)
		if c.want != 0 && resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	select {
	case <-s.StopRequested():
		t.Fatal("foreign origin was able to stop the server")
	default:
	}
	// nothing escaped root
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "etc")); err == nil {
		t.Fatal("write escaped root")
	}
	// server still healthy
	resp, err := http.Get(ts.URL + "/version")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("server unhealthy after bad posts: %v", err)
	}
}

func TestWriteReadRemoveUpload(t *testing.T) {
	s, ts, root := newTestServer(t)
	if r := post(t, ts.URL+"/w/a/b.txt", "", strings.NewReader("hello"), nil); r.StatusCode != 200 {
		t.Fatalf("write: %d", r.StatusCode)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a/b.txt")); string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
	resp, _ := http.Get(ts.URL + "/ls/a")
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"b.txt"`) {
		t.Fatalf("ls: %s", body)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("myFile", "../../pic.png")
	fw.Write([]byte("png"))
	mw.Close()
	if r := post(t, ts.URL+"/cmd/uploadFile", mw.FormDataContentType(), &buf, nil); r.StatusCode != 200 {
		t.Fatalf("upload: %d", r.StatusCode)
	}
	if m, _ := filepath.Glob(filepath.Join(root, "uploads", "*_pic.png")); len(m) != 1 {
		t.Fatalf("upload not stored in uploads/: %v", m)
	}
	s.SetReadOnly(true)
	if r := post(t, ts.URL+"/rm/a/b.txt", "", nil, nil); r.StatusCode != 403 {
		t.Fatalf("read-only rm: %d", r.StatusCode)
	}
	s.SetReadOnly(false)
	if r := post(t, ts.URL+"/rm/a/b.txt", "", nil, nil); r.StatusCode != 200 {
		t.Fatalf("rm: %d", r.StatusCode)
	}
}

func TestStopFromOwnPage(t *testing.T) {
	s, ts, _ := newTestServer(t)
	if r := post(t, ts.URL+"/cmd/stopServer", "application/json", strings.NewReader("{}"),
		map[string]string{"Origin": ts.URL}); r.StatusCode != 200 {
		t.Fatalf("stop: %d", r.StatusCode)
	}
	<-s.StopRequested()
}

func TestPanicIsRecovered(t *testing.T) {
	s, ts, _ := newTestServer(t)
	s.Mux().HandleFunc("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	resp, err := http.Post(ts.URL+"/boom", "", nil)
	if err != nil || resp.StatusCode != 500 {
		t.Fatalf("panic not recovered: %v", err)
	}
	if resp, err := http.Get(ts.URL + "/version"); err != nil || resp.StatusCode != 200 {
		t.Fatal("server died after panic")
	}
}

func TestWebVersionAndClient(t *testing.T) {
	web := t.TempDir()
	os.WriteFile(filepath.Join(web, "index.html"), []byte("app"), 0o644)
	s := New(Config{Root: t.TempDir(), WebFS: http.Dir(web), WebVersion: func() string { return "1.2.0" }})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Server-Timing"); got != `myweb-app;desc="1.2.0"` {
		t.Fatalf("Server-Timing: %q", got)
	}
	resp, err = http.Get(ts.URL + ClientPath)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") || !bytes.Contains(b, []byte("myweb:app-updated")) {
		t.Fatalf("client.js: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestProbesAreNotLogged(t *testing.T) {
	var got []string
	s := New(Config{Root: t.TempDir(), OnRequest: func(e RequestEvent) { got = append(got, e.Path) }})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	probe, _ := http.NewRequest("GET", ts.URL+"/cmd/status", nil)
	probe.Header.Set(ProbeHeader, "1")
	if resp, err := http.DefaultClient.Do(probe); err != nil || resp.StatusCode != 200 {
		t.Fatalf("probe: %v %v", resp, err)
	}
	http.Get(ts.URL + "/cmd/status")
	other, _ := http.NewRequest("GET", ts.URL+"/cmd/ls", nil)
	other.Header.Set(ProbeHeader, "1") // only status checks are quiet
	http.DefaultClient.Do(other)
	if strings.Join(got, " ") != "/cmd/status /cmd/ls" {
		t.Fatalf("logged: %v", got)
	}
}
