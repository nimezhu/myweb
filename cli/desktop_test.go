package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYWEB_HOME", root)
	got, err := DataFolder("myweb")
	if err != nil || got != filepath.Join(root, "data") {
		t.Fatalf("default: %q, %v", got, err)
	}
	os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"d": "~/somewhere"}`), 0o600)
	h, _ := os.UserHomeDir()
	if got, err := DataFolder("myweb"); err != nil || got != filepath.Join(h, "somewhere") {
		t.Fatalf("from the config: %q, %v", got, err)
	}
	if list, err := Servers("myweb"); err != nil || len(list) != 0 {
		t.Fatalf("Servers in an empty home: %v, %v", list, err)
	}
}
