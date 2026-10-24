package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/webapp"
)

func TestBumpVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.4.2 patch": "1.4.3", "1.4.2 minor": "1.5.0", "1.4.2 major": "2.0.0",
		"v1.4 patch": "v1.4.1", "1.4.2-beta.1 patch": "1.4.3", "2 minor": "2.1.0",
	} {
		f := strings.Fields(in)
		if got, err := bumpVersion(f[0], f[1]); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := bumpVersion("1.0.0", "huge"); err == nil {
		t.Error("unknown part accepted")
	}
}

func TestLoadProject(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, projectFile)
	os.WriteFile(cfg, []byte(`{"name":"hello","dir":"dist","out":"release","key":"app.key","keep":3}`), 0o644)
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	name, d, out, keep := fs.String("name", "", ""), fs.String("dir", "", ""), fs.String("out", "", ""), fs.Int("keep", 0, "")
	fs.String("key", "", "")
	fs.Parse([]string{"-out", "/elsewhere"})
	if err := loadProject(fs, cfg); err != nil {
		t.Fatal(err)
	}
	if *name != "hello" || *d != filepath.Join(dir, "dist") || *out != "/elsewhere" || *keep != 3 {
		t.Fatalf("name %q dir %q out %q keep %d", *name, *d, *out, *keep)
	}
	if err := loadProject(fs, filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing -config should fail")
	}
}

func TestBuildVersionList(t *testing.T) {
	dir := t.TempDir()
	dist, out, key := filepath.Join(dir, "dist"), filepath.Join(dir, "release"), filepath.Join(dir, "app.key")
	os.Mkdir(dist, 0o755)
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("v"), 0o644)
	if _, err := makeKey(key); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) error {
		return build(append([]string{"-key", key, "-name", "hello", "-dir", dist, "-out", out}, extra...))
	}
	readList := func() webapp.Versions {
		var l webapp.Versions
		b, _ := os.ReadFile(filepath.Join(out, "stable.versions.json"))
		json.Unmarshal(b, &l)
		return l
	}
	if err := run("-version", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	// a folder published before version lists existed: the next build starts
	// the list with what was published
	os.Remove(filepath.Join(out, "stable.versions.json"))
	os.Remove(filepath.Join(out, "stable.versions.json.sig"))
	if err := run("-bump", "patch", "-notes", "fix"); err != nil {
		t.Fatal(err)
	}
	if l := readList(); len(l.Versions) != 2 || l.Versions[0].Version != "1.0.1" || l.Versions[0].Notes != "fix" || l.Versions[1].Version != "1.0.0" {
		t.Fatalf("list: %+v", l)
	}
	// -keep drops the oldest from the list and the folder
	if err := run("-bump", "minor", "-keep", "2"); err != nil {
		t.Fatal(err)
	}
	if l := readList(); len(l.Versions) != 2 || l.Versions[0].Version != "1.1.0" {
		t.Fatalf("keep: %+v", l)
	}
	if _, err := os.Stat(filepath.Join(out, "hello-1.0.0.zip")); !os.IsNotExist(err) {
		t.Fatal("dropped zip still there")
	}
	pub, _ := os.ReadFile(key + ".pub")
	k, _ := webapp.ParsePublicKey(string(pub))
	if _, err := readRelease(k, filepath.Join(out, "stable.json")); err != nil {
		t.Fatal(err)
	}
	// a list edited after signing fails verify
	os.WriteFile(filepath.Join(out, "stable.versions.json"), []byte(`{"name":"hello","versions":[]}`), 0o644)
	if _, err := readRelease(k, filepath.Join(out, "stable.json")); err == nil {
		t.Fatal("tampered list verified")
	}
	if err := run("-version", "1.0.5"); err == nil {
		t.Fatal("an older version was accepted")
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, projectFile)
	if _, err := makeKey(filepath.Join(dir, "app.key")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cfg, []byte(`{"name":"hello","dir":"dist","out":"release","key":"app.key"}`), 0o644)
	read := func() project {
		var p project
		b, _ := os.ReadFile(cfg)
		json.Unmarshal(b, &p)
		return p
	}
	if err := rotateCmd([]string{"-config", cfg}); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.Key != "app-2.key" || strings.Join(p.OldKeys, ",") != "app.key" {
		t.Fatalf("rotate: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "app-2.key.pub")); err != nil {
		t.Fatal("new key not written next to the project")
	}
	if err := rotateCmd([]string{"-config", cfg}); err == nil {
		t.Fatal("a second rotation started before finishing")
	}
	if err := rotateCmd([]string{"-config", cfg, "-finish"}); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.Key != "app-2.key" || len(p.OldKeys) != 0 {
		t.Fatalf("finish: %+v", p)
	}
	if err := rotateCmd([]string{"-config", cfg}); err != nil || read().Key != "app-3.key" {
		t.Fatalf("next rotation: %v %+v", err, read())
	}
}

func TestFolderFlag(t *testing.T) {
	var f folderFlag
	for _, v := range []string{"/myapp", "samples:ro"} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if f.String() != "/myapp,/samples:ro" {
		t.Fatalf("%s", f.String())
	}
	for _, bad := range []string{"/", "../x", "/.myweb"} {
		if err := f.Set(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
