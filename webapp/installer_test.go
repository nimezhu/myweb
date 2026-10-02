package webapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// release is a fake distribution server.
type release struct {
	mu    sync.Mutex
	files map[string][]byte
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	ts    *httptest.Server
	down  bool
}

func newRelease(t *testing.T) *release {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &release{files: map[string][]byte{}, priv: priv, pub: pub}
	r.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		b, ok := r.files[strings.TrimPrefix(req.URL.Path, "/")]
		if r.down || !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(r.ts.Close)
	return r
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	zw.Close()
	return buf.Bytes()
}

// publish puts a version up; mutate can tamper with the manifest before signing.
func (r *release) publish(t *testing.T, version string, files map[string]string, mutate func(*Manifest)) {
	bundle := zipOf(t, files)
	sum := sha256.Sum256(bundle)
	m := Manifest{Name: "demo", Version: version, URL: "demo-" + version + ".zip",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bundle)), Published: time.Now()}
	if mutate != nil {
		mutate(&m)
	}
	raw, _ := m.Encode()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files["demo-"+version+".zip"] = bundle
	r.files["stable.json"] = raw
	r.files["stable.json.sig"] = Sign(r.priv, raw)
}

func (r *release) installer(t *testing.T, dir string, mod func(*Options)) *Installer {
	t.Helper()
	opts := Options{ManifestURL: r.ts.URL + "/stable.json", PublicKey: r.pub, Dir: dir, Myweb: "0.1.0"}
	if mod != nil {
		mod(&opts)
	}
	in, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func serve(t *testing.T, in *Installer, name string) string {
	t.Helper()
	f, err := in.FS().Open(name)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.IsDir() { // like http.FileServer: a folder serves its index.html
		return serve(t, in, strings.TrimSuffix(name, "/")+"/index.html")
	}
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestInstallUpdateAndKeep(t *testing.T) {
	r := newRelease(t)
	dir := t.TempDir()
	var updates []string
	in := r.installer(t, dir, func(o *Options) { o.OnUpdate = func(n, v string) { updates = append(updates, v) } })

	// nothing installed: a self-reloading placeholder, other files 404
	if s := serve(t, in, "/"); !strings.Contains(s, "Installing the app") || !strings.Contains(s, `http-equiv="refresh"`) {
		t.Fatalf("placeholder: %s", s)
	}
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1", "js/app.js": "one"}, nil)
	in.check(context.Background())
	if serve(t, in, "/index.html") != "v1" || serve(t, in, "/js/app.js") != "one" || in.Current() != "1.0.0" {
		t.Fatalf("install: %q %q", serve(t, in, "/index.html"), in.Current())
	}
	if !strings.Contains(in.StatusLine(), "demo 1.0.0 · up to date") {
		t.Fatalf("status: %s", in.StatusLine())
	}
	// same version again: nothing happens
	in.check(context.Background())
	// a newer version switches over
	r.publish(t, "1.1.0", map[string]string{"index.html": "v1.1"}, nil)
	in.check(context.Background())
	if serve(t, in, "/index.html") != "v1.1" || strings.Join(updates, ",") != "1.0.0,1.1.0" {
		t.Fatalf("update: %q %v", serve(t, in, "/index.html"), updates)
	}
	// keeps the newest 3
	for _, v := range []string{"1.2.0", "1.3.0", "1.10.0"} {
		r.publish(t, v, map[string]string{"index.html": v}, nil)
		in.check(context.Background())
	}
	if got := strings.Join(in.Installed(), ","); got != "1.10.0,1.3.0,1.2.0" || serve(t, in, "/") != "1.10.0" {
		t.Fatalf("kept: %s / %s", got, serve(t, in, "/"))
	}
	if _, err := os.Stat(filepath.Join(in.home, "1.0.0")); err == nil {
		t.Fatal("old version not pruned")
	}

	// a restart serves the installed version straight away, even offline
	r.down = true
	again := r.installer(t, dir, nil)
	if serve(t, again, "/") != "1.10.0" {
		t.Fatal("restart should serve the installed version")
	}
	again.check(context.Background())
	if serve(t, again, "/") != "1.10.0" || !strings.Contains(again.StatusLine(), "cannot check") {
		t.Fatalf("offline: %s", again.StatusLine())
	}
	// rollback: pin an installed version
	pinned := r.installer(t, dir, func(o *Options) { o.Pin = "1.2.0" })
	pinned.Start(context.Background())
	if serve(t, pinned, "/") != "1.2.0" || !strings.Contains(pinned.StatusLine(), "pinned") {
		t.Fatalf("pin: %s", pinned.StatusLine())
	}
	if _, err := New(Options{ManifestURL: r.ts.URL + "/stable.json", PublicKey: r.pub, Dir: dir, Pin: "0.9.0"}); err == nil {
		t.Fatal("pinning a version that isn't installed should fail")
	}
}

func TestRefusesTampering(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *release)
	}{
		{"wrong key", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, nil)
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			r.files["stable.json.sig"] = Sign(other, r.files["stable.json"])
		}},
		{"manifest changed after signing", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, nil)
			r.files["stable.json"] = bytes.Replace(r.files["stable.json"], []byte(`"1.0.0"`), []byte(`"9.0.0"`), 1)
		}},
		{"missing signature", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, nil)
			delete(r.files, "stable.json.sig")
		}},
		{"bundle swapped", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, nil)
			r.files["demo-1.0.0.zip"] = zipOf(t, map[string]string{"index.html": "evil"})
		}},
		{"zip slip", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x", "../../escape.txt": "evil"}, nil)
		}},
		{"needs a newer myweb", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, func(m *Manifest) { m.Requires.Myweb = ">=9.0.0" })
		}},
		{"bad name", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, func(m *Manifest) { m.Name = "../evil" })
		}},
		{"insecure bundle url", func(t *testing.T, r *release) {
			r.publish(t, "1.0.0", map[string]string{"index.html": "x"}, func(m *Manifest) { m.URL = "http://example.com/x.zip" })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRelease(t)
			c.setup(t, r)
			dir := t.TempDir()
			in := r.installer(t, dir, nil)
			in.check(context.Background())
			if in.Current() != "" || !strings.Contains(serve(t, in, "/"), "Installing the app") {
				t.Fatalf("installed despite %s: %s", c.name, in.StatusLine())
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
				t.Fatal("zip escaped")
			}
		})
	}
}

func TestNoDowngradeAndSingleFolder(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t, t.TempDir(), nil)
	r.publish(t, "2.0.0", map[string]string{"dist/index.html": "two", "dist/a.js": "a"}, nil)
	in.check(context.Background())
	if serve(t, in, "/") != "two" || serve(t, in, "/a.js") != "a" {
		t.Fatalf("single top-level folder should be served: %q", serve(t, in, "/"))
	}
	// an older signed manifest replayed by the server is ignored
	r.publish(t, "1.0.0", map[string]string{"index.html": "old"}, nil)
	in.check(context.Background())
	if in.Current() != "2.0.0" {
		t.Fatalf("downgraded to %s", in.Current())
	}
}

func TestOptionsValidation(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, u := range []string{"http://example.com/s.json", "ftp://x/s.json", "s.json"} {
		if _, err := New(Options{ManifestURL: u, PublicKey: pub, Dir: t.TempDir()}); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	for _, u := range []string{"https://example.com/s.json", "http://127.0.0.1:9/s.json", "http://localhost/s.json"} {
		if _, err := New(Options{ManifestURL: u, PublicKey: pub, Dir: t.TempDir()}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	if _, err := New(Options{ManifestURL: "https://example.com/s.json", Dir: t.TempDir()}); err == nil {
		t.Error("a key is required")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.9", 1}, {"1.2", "1.2.0", 0}, {"v2.0.0", "1.99", 1},
		{"1.2.0-beta", "1.2.0", -1}, {"1.2.0-beta.2", "1.2.0-beta.1", 1}, {"0.1.0", "0.1.0", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !satisfies("0.2.0", ">=0.1.0") || satisfies("0.1.0", ">=0.2.0") || !satisfies("1.0.0", "") {
		t.Error("satisfies")
	}
}

// Through http.FileServer, as myweb serves it: "/" must work before and
// after the first install.
func TestServedByFileServer(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t, t.TempDir(), nil)
	ts := httptest.NewServer(http.FileServer(in.FS()))
	defer ts.Close()
	get := func(p string) (int, string) {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/"); code != 200 || !strings.Contains(body, "Installing the app") {
		t.Fatalf("placeholder via FileServer: %d %q", code, body)
	}
	if code, _ := get("/app.js"); code != 404 {
		t.Fatalf("other files before install: %d", code)
	}
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1", "app.js": "js"}, nil)
	in.check(context.Background())
	if code, body := get("/"); code != 200 || body != "v1" {
		t.Fatalf("installed via FileServer: %d %q", code, body)
	}
	if _, body := get("/app.js"); body != "js" {
		t.Fatal("asset")
	}
}

func TestNoFolderUntilInstalled(t *testing.T) {
	r := newRelease(t)
	dir := t.TempDir()
	in := r.installer(t, dir, nil)
	in.check(context.Background()) // nothing published: 404
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("created %v before installing anything", entries)
	}
}
