package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/selfupdate"
	"github.com/nimezhu/myweb/webapp"
)

func TestUpgradeCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.0.2", "html_url": "https://example/rel"})
	}))
	defer srv.Close()
	selfupdate.API = srv.URL
	defer func() { selfupdate.API = "https://api.github.com" }()
	exe := filepath.Join(t.TempDir(), "myweb")
	os.WriteFile(exe, []byte("x"), 0o755)
	executable = func() (string, error) { return exe, nil }
	defer func() { executable = defaultExecutable }()
	current := "0.0.1"
	newUpdater = func(p string) *selfupdate.Updater { return &selfupdate.Updater{Program: p, Current: current} }
	defer func() { newUpdater = defaultUpdater }()

	spec := Spec{Name: "myweb"}
	var out strings.Builder
	if err := upgradeCmd(spec, []string{"-check"}, &out); err != nil || !strings.Contains(out.String(), "myweb 0.0.2 is available") {
		t.Fatalf("-check: %q, %v", out.String(), err)
	}
	current = "0.0.2"
	out.Reset()
	if err := upgradeCmd(spec, nil, &out); err != nil || !strings.Contains(out.String(), "is the latest") {
		t.Fatalf("latest: %q, %v", out.String(), err)
	}
	for _, args := range [][]string{{"-check", "-rollback"}, {"0.0.1", "0.0.2"}, {"0.0.1", "-check"}, {"-nope"}} {
		if _, ok := upgradeCmd(spec, args, &out).(usageError); !ok {
			t.Errorf("%v: not a usage error", args)
		}
	}
	if err := upgradeCmd(spec, []string{"-rollback"}, &out); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Errorf("-rollback without .old: %v", err)
	}
}

// myweb-publish and myweb-team upgrade from myweb's releases, each to its own
// binary; myweb-publish runs no servers, so it restarts nothing.
func TestUpgradeTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script plays the new binary")
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	asset := func(p string) string { return p + "_0.0.9_" + runtime.GOOS + "_" + runtime.GOARCH }
	bins := map[string]string{}
	sums := ""
	for _, p := range []string{"myweb", "myweb-publish"} {
		bins[asset(p)] = "#!/bin/sh\necho " + p + " 0.0.9\n"
		h := sha256.Sum256([]byte(bins[asset(p)]))
		sums += hex.EncodeToString(h[:]) + "  " + asset(p) + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/releases/latest"):
			assets := []map[string]string{{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
				{"name": "checksums.txt.sig", "browser_download_url": srv.URL + "/dl/checksums.txt.sig"}}
			for n := range bins {
				assets = append(assets, map[string]string{"name": n, "browser_download_url": srv.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.0.9", "html_url": "page", "assets": assets})
		case p == "/dl/checksums.txt":
			io.WriteString(w, sums)
		case p == "/dl/checksums.txt.sig":
			w.Write(webapp.Sign([]byte(sums), key))
		default:
			io.WriteString(w, bins[strings.TrimPrefix(p, "/dl/")])
		}
	}))
	defer srv.Close()
	selfupdate.API = srv.URL
	defer func() { selfupdate.API = "https://api.github.com" }()
	exe := filepath.Join(t.TempDir(), "myweb-publish")
	os.WriteFile(exe, []byte("old"), 0o755)
	executable = func() (string, error) { return exe, nil }
	defer func() { executable = defaultExecutable }()
	newUpdater = func(p string) *selfupdate.Updater {
		return &selfupdate.Updater{Program: p, Current: "0.0.3", Keys: []string{webapp.EncodePublicKey(pub)}}
	}
	defer func() { newUpdater = defaultUpdater }()
	services = func(string, string) []*service { t.Error("myweb-publish looked for services"); return nil }
	defer func() { services = installedServices }()

	var out strings.Builder
	if err := upgradeCmd(Spec{Name: "myweb-publish", NoServers: true}, nil, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != bins[asset("myweb-publish")] {
		t.Fatalf("installed %q", b)
	}
	if !strings.Contains(out.String(), "myweb-publish 0.0.3 → 0.0.9") {
		t.Errorf("output %q", out.String())
	}
}
