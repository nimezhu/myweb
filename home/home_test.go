package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFor(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	t.Setenv("USERPROFILE", fake) // Windows
	t.Setenv("MYWEB_HOME", "")
	d, err := For("myweb")
	if err != nil || d.Root != filepath.Join(fake, ".myweb") {
		t.Fatalf("default: %v %v", d, err)
	}
	if d.Data() != filepath.Join(fake, ".myweb", "data") || d.Apps() != filepath.Join(fake, ".myweb", "apps") {
		t.Fatal("subfolders")
	}
	c, _ := For("cellome")
	if c.Root != filepath.Join(fake, ".cellome") {
		t.Fatalf("cellome: %v", c.Root)
	}
	other := t.TempDir()
	t.Setenv("CELLOME_HOME", other)
	if c, _ := For("cellome"); c.Root != other {
		t.Fatalf("CELLOME_HOME: %v", c.Root)
	}
	if EnvVar("myweb") != "MYWEB_HOME" {
		t.Fatal("env var name")
	}
}

func TestEnsureData(t *testing.T) {
	d := Dirs{filepath.Join(t.TempDir(), ".myweb")}
	p, created, err := d.EnsureData()
	if err != nil || !created || p != d.Data() {
		t.Fatalf("first: %v %v %v", p, created, err)
	}
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Fatal("not created")
	}
	if _, created, err := d.EnsureData(); err != nil || created {
		t.Fatalf("second: %v %v", created, err)
	}
	f := Dirs{t.TempDir()}
	os.WriteFile(f.Data(), []byte("x"), 0o644)
	if _, _, err := f.EnsureData(); err == nil {
		t.Fatal("a file in the way must be an error")
	}
}
