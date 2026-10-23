package webapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPublicKey(t *testing.T) {
	pub, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "app.key.pub")
	if err := os.WriteFile(file, []byte(pub+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{pub, " " + pub + "\n", "ed25519:" + pub, file} {
		k, err := LoadPublicKey(s)
		if err != nil {
			t.Fatalf("LoadPublicKey(%q): %v", s, err)
		}
		if EncodePublicKey(k) != pub {
			t.Fatalf("LoadPublicKey(%q) = %s, want %s", s, EncodePublicKey(k), pub)
		}
	}
	for _, s := range []string{"", "not-a-key", filepath.Join(t.TempDir(), "missing.pub")} {
		if _, err := LoadPublicKey(s); err == nil {
			t.Fatalf("LoadPublicKey(%q) succeeded", s)
		}
	}
}
