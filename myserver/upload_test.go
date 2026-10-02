package myserver

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type part struct{ field, name, body string }

func multipartBody(parts ...part) (io.Reader, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.name == "" {
			mw.WriteField(p.field, p.body)
			continue
		}
		fw, _ := mw.CreateFormFile(p.field, p.name)
		fw.Write([]byte(p.body))
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func upload(t *testing.T, url string, parts ...part) (*http.Response, map[string]interface{}) {
	t.Helper()
	body, ctype := multipartBody(parts...)
	resp, err := http.Post(url, ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&j)
	return resp, j
}

func paths(j map[string]interface{}) []string {
	var out []string
	files, _ := j["files"].([]interface{})
	for _, f := range files {
		out = append(out, f.(map[string]interface{})["path"].(string))
	}
	return out
}

func newUploadServer(t *testing.T, cfg Config) (*Server, string, string) {
	cfg.Root = t.TempDir()
	s := New(cfg)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return s, ts.URL, cfg.Root
}

func readFile(root, p string) string {
	b, err := os.ReadFile(filepath.Join(root, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestUpManyFilesAndFolders(t *testing.T) {
	_, url, root := newUploadServer(t, Config{})
	r, j := upload(t, url+"/up/docs",
		part{"file", "a.txt", "A"},
		part{"file", "photos/2024/b.jpg", "B"},
		part{"file", "sub/file", "named file"},
		part{"file", "../../escape.txt", "E"},
		part{"note", "", "a plain field is ignored"},
	)
	got := strings.Join(paths(j), ",")
	if r.StatusCode != 200 || got != "/docs/a.txt,/docs/photos/2024/b.jpg,/docs/sub/file,/docs/escape.txt" {
		t.Fatalf("upload: %d %v", r.StatusCode, j)
	}
	if readFile(root, "docs/photos/2024/b.jpg") != "B" || readFile(root, "docs/sub/file") != "named file" {
		t.Fatal("contents")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("escaped the root")
	}
	// no temp files left behind
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if strings.Contains(p, ".upload.tmp") {
			t.Errorf("leftover temp file %s", p)
		}
		return nil
	})
	f := j["files"].([]interface{})[0].(map[string]interface{})
	if f["url"] != "/f/docs/a.txt" || f["etag"] == "" || f["size"].(float64) != 1 {
		t.Fatalf("file info: %v", f)
	}
}

func TestUpConflicts(t *testing.T) {
	s, url, root := newUploadServer(t, Config{})
	upload(t, url+"/up/", part{"f", "a.txt", "v1"})
	// default: rename
	_, j := upload(t, url+"/up/", part{"f", "a.txt", "v2"}, part{"f", "a.txt", "v3"})
	if got := strings.Join(paths(j), ","); got != "/a (1).txt,/a (2).txt" || readFile(root, "a.txt") != "v1" {
		t.Fatalf("rename: %v", got)
	}
	if j["files"].([]interface{})[0].(map[string]interface{})["renamed"] != true {
		t.Fatal("renamed flag")
	}
	// fail
	if r, j := upload(t, url+"/up/?onconflict=fail", part{"f", "new.txt", "n"}, part{"f", "a.txt", "x"}); r.StatusCode != 409 || len(paths(j)) != 1 {
		t.Fatalf("fail: %d %v", r.StatusCode, j)
	}
	// overwrite keeps the old content as a version
	if r, _ := upload(t, url+"/up/?onconflict=overwrite", part{"f", "a.txt", "v4"}); r.StatusCode != 200 || readFile(root, "a.txt") != "v4" {
		t.Fatalf("overwrite: %d", r.StatusCode)
	}
	if vs := listVersions(s.historyDir("/a.txt")); len(vs) != 1 {
		t.Fatalf("versions after overwrite: %d", len(vs))
	}
	os.Mkdir(filepath.Join(root, "dir"), 0o755)
	if r, _ := upload(t, url+"/up/?onconflict=overwrite", part{"f", "dir", "x"}); r.StatusCode != 409 {
		t.Fatalf("file over folder: %d", r.StatusCode)
	}
	if r, _ := upload(t, url+"/up/?onconflict=bogus", part{"f", "x", "x"}); r.StatusCode != 400 {
		t.Fatalf("bad mode: %d", r.StatusCode)
	}
}

func TestUpLimitsAndGuards(t *testing.T) {
	s, url, root := newUploadServer(t, Config{MaxUpload: 10})
	if r, _ := upload(t, url+"/up/", part{"f", "big.bin", strings.Repeat("x", 11)}); r.StatusCode != 413 {
		t.Fatalf("too large: %d", r.StatusCode)
	}
	if r, _ := upload(t, url+"/up/", part{"f", "ok.bin", strings.Repeat("x", 10)}); r.StatusCode != 200 {
		t.Fatalf("at the limit: %d", r.StatusCode)
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload.tmp") || e.Name() == "big.bin" {
			t.Fatalf("leftover %s", e.Name())
		}
	}
	for _, c := range []struct{ url, name string }{
		{"/up/.myweb", "x"}, {"/up/", ".myweb/x"},
	} {
		if r, _ := upload(t, url+c.url, part{"f", c.name, "x"}); r.StatusCode != 403 {
			t.Errorf("%s %s: %d", c.url, c.name, r.StatusCode)
		}
	}
	if r, _ := upload(t, url+"/up/", part{"field", "", "no files"}); r.StatusCode != 400 {
		t.Fatalf("no files: %d", r.StatusCode)
	}
	if resp, _ := http.Post(url+"/up/", "text/plain", strings.NewReader("x")); resp.StatusCode != 400 {
		t.Fatalf("not multipart: %d", resp.StatusCode)
	}
	s.SetReadOnly(true)
	if r, _ := upload(t, url+"/up/", part{"f", "a", "x"}); r.StatusCode != 403 {
		t.Fatalf("read-only: %d", r.StatusCode)
	}
}

func TestLegacyUploadFileStreams(t *testing.T) {
	_, url, root := newUploadServer(t, Config{MaxUpload: 1 << 20})
	content := strings.Repeat("0123456789", 50000) // 500 KB
	r, j := upload(t, url+"/cmd/uploadFile", part{"other", "x.txt", "ignored"}, part{"myFile", "../pic.png", content})
	f, _ := j["file"].(map[string]interface{})
	want := fmt.Sprintf("%x_pic.png", md5.Sum([]byte(content)))
	if r.StatusCode != 200 || j["success"].(float64) != 1 || f["name"] != want || f["url"] != "/f/uploads/"+want {
		t.Fatalf("legacy upload: %d %v", r.StatusCode, j)
	}
	if readFile(root, "uploads/"+want) != content {
		t.Fatal("content")
	}
	if r, _ := upload(t, url+"/cmd/uploadFile", part{"myFile", "big", strings.Repeat("x", 2<<20)}); r.StatusCode != 413 {
		t.Fatalf("legacy too large: %d", r.StatusCode)
	}
	if r, _ := upload(t, url+"/cmd/uploadFile", part{"nope", "a", "x"}); r.StatusCode != 400 {
		t.Fatalf("legacy missing field: %d", r.StatusCode)
	}
}
