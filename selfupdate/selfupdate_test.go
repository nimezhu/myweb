package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/webapp"
)

// testKey signs the fake releases; the Updaters of the tests trust it.
var testPub, testKey, _ = ed25519.GenerateKey(rand.Reader)

func updater(goos, goarch string) *Updater {
	return &Updater{Program: "myweb", Current: "0.0.1", GOOS: goos, GOARCH: goarch, Keys: []string{webapp.EncodePublicKey(testPub)}}
}

// fakeGitHub serves one release, v0.0.2, with the binaries given by asset
// name, and checksums.txt signed with testKey.
func fakeGitHub(t *testing.T, bins map[string]string, sums string) *httptest.Server {
	return fakeSigned(t, bins, sums, string(webapp.Sign([]byte(sums), testKey)))
}

// fakeSigned is fakeGitHub with the signature given ("" for none).
func fakeSigned(t *testing.T, bins map[string]string, sums, sig string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+Repo+"/releases/latest", r.URL.Path == "/repos/"+Repo+"/releases/tags/v0.0.2":
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			assets := []asset{{"checksums.txt", srv.URL + "/dl/checksums.txt"}}
			if sig != "" {
				assets = append(assets, asset{"checksums.txt.sig", srv.URL + "/dl/checksums.txt.sig"})
			}
			for n := range bins {
				assets = append(assets, asset{n, srv.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.0.2", "body": "notes", "html_url": "page", "assets": assets})
		case r.URL.Path == "/dl/checksums.txt":
			fmt.Fprint(w, sums)
		case r.URL.Path == "/dl/checksums.txt.sig":
			fmt.Fprint(w, sig)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			b, ok := bins[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	API = srv.URL
	t.Cleanup(func() { API = "https://api.github.com" })
	return srv
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestInstallAndRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the new binary")
	}
	u := updater("", "")
	name := u.AssetName("0.0.2")
	bin := "#!/bin/sh\necho myweb 0.0.2\n"
	fakeGitHub(t, map[string]string{name: bin}, sum(bin)+"  "+name+"\n"+sum("x")+"  other\n")

	exe := filepath.Join(t.TempDir(), "myweb")
	os.WriteFile(exe, []byte("old"), 0o755)
	rel, err := u.Latest(context.Background())
	if err != nil || rel.Version != "0.0.2" || rel.Notes != "notes" {
		t.Fatalf("Latest = %+v, %v", rel, err)
	}
	if err := u.Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != bin {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old" {
		t.Fatalf("exe.old = %q", b)
	}
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal(".new left behind")
	}
	if err := Rollback(exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("after rollback exe = %q", b)
	}
	if err := Rollback(exe); err != nil { // and forward again
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != bin {
		t.Fatalf("after second rollback exe = %q", b)
	}
}

func TestInstallRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the new binary")
	}
	u := updater("", "")
	name := u.AssetName("0.0.2")
	for _, c := range []struct{ what, bin, sums, err string }{
		{"bad checksum", "#!/bin/sh\necho myweb 0.0.2\n", sum("other") + "  " + name + "\n", "checksum"},
		{"no checksum", "#!/bin/sh\necho myweb 0.0.2\n", "", "no sum"},
		{"wrong version", "#!/bin/sh\necho myweb 0.0.3\n", "", "expected version"},
		{"does not run", "#!/bin/sh\nexit 3\n", "", "does not run"},
	} {
		sums := c.sums
		if sums == "" && c.err != "no sum" {
			sums = sum(c.bin) + "  " + name + "\n"
		}
		fakeGitHub(t, map[string]string{name: c.bin}, sums)
		exe := filepath.Join(t.TempDir(), "myweb")
		os.WriteFile(exe, []byte("old"), 0o755)
		rel, err := u.Latest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = u.Install(context.Background(), rel, exe)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err = %v, want %q", c.what, err, c.err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" {
			t.Errorf("%s: exe was replaced", c.what)
		}
	}
}

func TestNoAssetForPlatform(t *testing.T) {
	u := updater("plan9", "386")
	fakeGitHub(t, map[string]string{"myweb_0.0.2_linux_amd64": "x"}, "")
	rel, _ := u.Latest(context.Background())
	err := u.Install(context.Background(), rel, filepath.Join(t.TempDir(), "myweb"))
	if err == nil || !strings.Contains(err.Error(), "myweb_0.0.2_plan9_386") {
		t.Fatalf("err = %v", err)
	}
	if _, err := u.Get(context.Background(), "9.9.9"); err == nil {
		t.Fatal("Get of a missing release worked")
	}
}

func TestCached(t *testing.T) {
	fakeGitHub(t, nil, "")
	u := updater("", "")
	file := filepath.Join(t.TempDir(), "check.json")
	if v := u.Cached(context.Background(), file, time.Hour); v != "0.0.2" {
		t.Fatalf("Cached = %q", v)
	}
	API = "http://127.0.0.1:1" // unreachable: the cached answer is used
	if v := u.Cached(context.Background(), file, time.Hour); v != "0.0.2" {
		t.Fatalf("Cached from the file = %q", v)
	}
	if v := u.Cached(context.Background(), file, 0); v != "" {
		t.Fatalf("expired and unreachable = %q", v)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.0.2", "0.0.1", true},
		{"0.0.1", "0.0.1", false},
		{"0.0.10", "0.0.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0-rc.1", true},
		{"0.1.0-rc.1", "0.1.0", false},
		{"v0.2.0", "0.1.9", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestSwapOldInUse(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "myweb")
	os.WriteFile(exe, []byte("two"), 0o755)
	os.WriteFile(exe+".new", []byte("three"), 0o755)
	// an .old that can't be removed (as a running binary on Windows): a
	// folder that isn't empty
	os.MkdirAll(filepath.Join(exe+".old", "x"), 0o755)
	os.MkdirAll(exe+".old.1.del", 0o755) // from an earlier upgrade, no longer running
	if err := swap(exe, exe+".new"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "three" {
		t.Errorf("exe: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "two" {
		t.Errorf("old: %q", b)
	}
	if _, err := os.Stat(exe + ".old.1.del"); !os.IsNotExist(err) {
		t.Error("the earlier one was not removed")
	}
	if aside, _ := filepath.Glob(exe + ".old.*.del"); len(aside) != 1 {
		t.Errorf("moved aside: %v", aside)
	}
}

func TestInstallRefusesUnsigned(t *testing.T) {
	u := updater("", "")
	name := u.AssetName("0.0.2")
	bin := "#!/bin/sh\necho myweb 0.0.2\n"
	sums := sum(bin) + "  " + name + "\n"
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	for what, sig := range map[string]string{
		"unsigned":        "",
		"another key":     string(webapp.Sign([]byte(sums), otherKey)),
		"other checksums": string(webapp.Sign([]byte(sums+"x"), testKey)),
		"not a signature": "nope\n",
	} {
		fakeSigned(t, map[string]string{name: bin}, sums, sig)
		exe := filepath.Join(t.TempDir(), "myweb")
		os.WriteFile(exe, []byte("old"), 0o755)
		rel, _ := u.Latest(context.Background())
		err := u.Install(context.Background(), rel, exe)
		if err == nil || !strings.Contains(err.Error(), "not installing it") {
			t.Errorf("%s: err = %v", what, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" {
			t.Errorf("%s: exe was replaced", what)
		}
	}
	if len(Keys) == 0 {
		t.Error("no release key built in")
	}
}
