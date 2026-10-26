package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/home"
)

func TestEvery(t *testing.T) {
	for in, want := range map[string]string{"1d": "1d", "2w": "2w", "12h": "12h0m0s", "off": "off", "": "off", "48h": "2d", "30m": "30m0s"} {
		var e every
		if err := e.Set(in); err != nil || e.String() != want {
			t.Errorf("%q: %v %q, want %q", in, err, e.String(), want)
		}
	}
	for _, bad := range []string{"x", "-1d", "0d", "5m", "1y"} {
		var e every
		if err := e.Set(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestAutoBackup(t *testing.T) {
	spec := Spec{Name: "myweb"}
	root := t.TempDir()
	dirs := home.Dirs{Root: root}
	os.MkdirAll(filepath.Join(root, "data", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "data", "sub", "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(root, "config.json"), []byte("{}"), 0o600)
	a := &autoBackup{spec: spec, dirs: dirs, data: dirs.Data(), dir: filepath.Join(root, "backups"), every: 24 * time.Hour, keep: 2}

	t0 := time.Now()
	out, err := a.due(t0)
	if err != nil || out == "" {
		t.Fatalf("first: %q %v", out, err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	zr.Close()
	got := strings.Join(names, " ")
	if !strings.Contains(got, "data/sub/a.txt") || strings.Contains(got, "backups") {
		t.Fatalf("in the zip: %s", got)
	}
	if out, _ := a.due(t0.Add(time.Hour)); out != "" {
		t.Fatal("made again before it was due")
	}
	for i := 1; i <= 3; i++ { // later ones: two are kept
		now := t0.Add(time.Duration(i) * 25 * time.Hour)
		out, err := a.due(now)
		if err != nil || out == "" {
			t.Fatalf("day %d: %v", i, err)
		}
		os.Chtimes(out, now, now)
	}
	if n := len(autoBackups("myweb", a.dir)); n != 2 {
		t.Fatalf("kept %d", n)
	}
	if st, ok := readAutoBackupStatus(a.dir); !ok || st.Error != "" || st.File == "" {
		t.Fatalf("status: %+v", st)
	}

	// another server is making one: this one waits
	os.WriteFile(filepath.Join(a.dir, ".backup.lock"), []byte("1"), 0o600)
	if out, err := a.due(t0.Add(10 * 24 * time.Hour)); out != "" || err != nil {
		t.Fatalf("with a lock: %q %v", out, err)
	}
	old := time.Now().Add(-7 * time.Hour) // a stale lock is taken over
	os.Chtimes(filepath.Join(a.dir, ".backup.lock"), old, old)
	if out, err := a.due(t0.Add(10 * 24 * time.Hour)); out == "" || err != nil {
		t.Fatalf("stale lock: %q %v", out, err)
	}
}

func TestBackupSchedule(t *testing.T) {
	spec := Spec{Name: "myweb"}
	root := t.TempDir()
	dirs := home.Dirs{Root: root}
	var out bytes.Buffer
	if err := backupCmd(spec, []string{"-home", root, "-every", "1d", "-keep", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "every 1d") || !strings.Contains(out.String(), "keeping the newest 3") {
		t.Fatalf("said: %s", out.String())
	}
	b, _ := os.ReadFile(configFile(dirs))
	var c map[string]string
	json.Unmarshal(b, &c)
	if c["backup-every"] != "1d" || c["backup-keep"] != "3" {
		t.Fatalf("config: %s", b)
	}
	sf, err := configured(spec, dirs)
	if err != nil || time.Duration(*sf.backupEvery) != 24*time.Hour || *sf.backupKeep != 3 {
		t.Fatalf("served with: %v", err)
	}

	out.Reset()
	backupCmd(spec, []string{"-home", root, "-list"}, &out)
	if !strings.Contains(out.String(), "every 1d") {
		t.Fatalf("list: %s", out.String())
	}

	var doc bytes.Buffer
	d := &doctor{w: &doc}
	d.backups(spec, dirs, sf)
	if !strings.Contains(doc.String(), "none made yet") {
		t.Fatalf("doctor: %s", doc.String())
	}

	out.Reset()
	if err := backupCmd(spec, []string{"-home", root, "-every", "off"}, &out); err != nil || !strings.Contains(out.String(), "off") {
		t.Fatalf("off: %v %s", err, out.String())
	}
	if err := backupCmd(spec, []string{"-home", root, "-keep", "3"}, &out); err == nil {
		t.Fatal("-keep alone")
	}
	if err := backupCmd(spec, []string{"-home", root, "-every", "soon"}, &out); err == nil {
		t.Fatal("bad -every")
	}
}
