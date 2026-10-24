package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRestore(t *testing.T) {
	spec := Spec{Name: "myweb"}
	src := t.TempDir()
	files := map[string]string{
		"config.json":                      `{"auth":true}`,
		"apps/abc/state.json":              "{}",
		"run/1.json":                       "running",
		"data/notes.txt":                   "notes",
		"data/sub/a.csv":                   "1,2",
		"data/.myweb/history/notes.txt/v1": "old notes",
		"data/.myweb/shares.json":          "[]",
	}
	for p, body := range files {
		os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(src, p), []byte(body), 0o644)
	}
	os.Symlink("/etc", filepath.Join(src, "data", "link"))
	zipFile := filepath.Join(t.TempDir(), "b.zip")
	var out bytes.Buffer
	if err := backupCmd(spec, []string{"-home", src, zipFile}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "6 files") || !strings.Contains(out.String(), "1 symbolic link left out") {
		t.Errorf("backup said:\n%s", out.String())
	}
	if fi, _ := os.Stat(zipFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("zip mode %v", fi.Mode().Perm())
	}

	// into a new home: everything but run/
	dst := filepath.Join(t.TempDir(), "h")
	out.Reset()
	if err := restoreCmd(spec, []string{"-home", dst, zipFile}, &out); err != nil {
		t.Fatal(err)
	}
	for p, body := range files {
		b, err := os.ReadFile(filepath.Join(dst, p))
		if strings.HasPrefix(p, "run/") {
			if err == nil {
				t.Error("run/ restored")
			}
			continue
		}
		if string(b) != body {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, "data", "link")); err == nil {
		t.Error("symlink restored")
	}

	// again: nothing differs; then a changed file is kept, unless -force
	out.Reset()
	restoreCmd(spec, []string{"-home", dst, zipFile}, &out)
	if !strings.Contains(out.String(), "restored 0 files; 6 already there") {
		t.Errorf("second restore:\n%s", out.String())
	}
	os.WriteFile(filepath.Join(dst, "data/notes.txt"), []byte("mine"), 0o644)
	out.Reset()
	restoreCmd(spec, []string{"-home", dst, zipFile}, &out)
	if b, _ := os.ReadFile(filepath.Join(dst, "data/notes.txt")); string(b) != "mine" || !strings.Contains(out.String(), "1 file differs") {
		t.Errorf("a changed file was replaced (%q):\n%s", b, out.String())
	}
	restoreCmd(spec, []string{"-home", dst, "-dry-run", "-force", zipFile}, &out)
	if b, _ := os.ReadFile(filepath.Join(dst, "data/notes.txt")); string(b) != "mine" {
		t.Error("-dry-run wrote")
	}
	restoreCmd(spec, []string{"-home", dst, "-force", zipFile}, &out)
	if b, _ := os.ReadFile(filepath.Join(dst, "data/notes.txt")); string(b) != "notes" {
		t.Error("-force didn't replace it")
	}

	// -no-files: only the data folder's .myweb
	out.Reset()
	small := filepath.Join(t.TempDir(), "s.zip")
	if err := backupCmd(spec, []string{"-home", src, "-no-files", small}, &out); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.OpenReader(small)
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "data/") && !strings.HasPrefix(f.Name, "data/.myweb") {
			t.Errorf("-no-files has %s", f.Name)
		}
	}
}

func TestRestoreRefusesEscapes(t *testing.T) {
	for _, name := range []string{"home/../../x", "../x", "/etc/x", "home\\..\\x", "data/a/../../../x", "other/x", "home/run/1.json", "home"} {
		if p, ok := restoreTarget(name, "/h", "/d"); ok {
			t.Errorf("%s → %s", name, p)
		}
	}
	if p, ok := restoreTarget("data/a/b.txt", "/h", "/d"); !ok || p != filepath.Join("/d", "a", "b.txt") {
		t.Errorf("data/a/b.txt → %s %v", p, ok)
	}
	// a zip of something else
	zipFile := filepath.Join(t.TempDir(), "x.zip")
	f, _ := os.Create(zipFile)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("home/config.json")
	w.Write([]byte("{}"))
	zw.Close()
	f.Close()
	if err := restoreCmd(Spec{Name: "myweb"}, []string{"-home", t.TempDir(), zipFile}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a myweb backup") {
		t.Errorf("err = %v", err)
	}
}
