package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/selfupdate"
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
