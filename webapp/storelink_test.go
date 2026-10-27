package webapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLink(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := EncodePublicKey(pub)
	l := StoreLink{URL: "https://store.example/io/apps.json", Key: key, Name: "Example apps"}
	s := l.String()
	if !strings.HasPrefix(s, "https://store.example/io/apps.json#key=") {
		t.Fatalf("link: %s", s)
	}
	for _, in := range []string{
		s,
		"  " + s + "\n",
		// pasted with the key unescaped: its + read as spaces
		"https://store.example/io/apps.json#key=" + key + "&name=Example%20apps",
		"myweb://add-store?url=" + "https%3A%2F%2Fstore.example%2Fio%2Fapps.json" + "&key=" + strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D").Replace(key) + "&name=Example+apps",
	} {
		got, err := ParseStoreLink(in)
		if err != nil || got != l {
			t.Errorf("ParseStoreLink(%q) = %+v, %v", in, got, err)
		}
	}
	for in, want := range map[string]string{
		"":                                   "isn't a store link",
		"https://store.example/io/apps.json": "no key",
		"http://store.example/io/apps.json#key=" + key: "https",
		"https://store.example/io/apps.json#key=abc":   "damaged",
	} {
		if _, err := ParseStoreLink(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseStoreLink(%q): %v, want %q", in, err, want)
		}
	}
	if fp := KeyFingerprint(key); len(fp) != 14 || fp != KeyFingerprint(key) || fp == KeyFingerprint(EncodePublicKey(make([]byte, 32))) {
		t.Errorf("fingerprint %q", fp)
	}
	if got := SplitStoreLinks("# ours\n" + s + "\n\n  " + s + " " + s + "\n"); len(got) != 3 {
		t.Errorf("SplitStoreLinks: %q", got)
	}
}

// Default stores are added to a home once: one the user removes stays removed.
func TestSeed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a := StoreLink{URL: "https://a.example/apps.json", Key: EncodePublicKey(pub), Name: "A"}.String()
	b := StoreLink{URL: "https://b.example/apps.json", Key: EncodePublicKey(pub)}.String()
	cats := newCatalogs(t)
	cats.Seeded = filepath.Join(filepath.Dir(cats.File), "catalogs-seeded.json")
	if err := cats.Seed([]string{a}); err != nil {
		t.Fatal(err)
	}
	srcs, _ := cats.Sources()
	if len(srcs) != 1 || srcs[0].URL != "https://a.example/apps.json" || srcs[0].Name != "A" {
		t.Fatalf("seeded: %+v", srcs)
	}
	if err := cats.Remove("https://a.example/apps.json"); err != nil {
		t.Fatal(err)
	}
	if err := cats.Seed([]string{a, b, "not a link"}); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("a bad link: %v", err)
	}
	srcs, _ = cats.Sources()
	if len(srcs) != 1 || srcs[0].URL != "https://b.example/apps.json" {
		t.Fatalf("after removing a, seeding a and b: %+v", srcs)
	}
}

func TestCatalogPreview(t *testing.T) {
	r := newRelease(t)
	catPub, catPriv, _ := ed25519.GenerateKey(rand.Reader)
	r.putCatalog(t, Catalog{Title: "Test apps", Apps: []CatalogApp{{Name: "demo", Manifest: r.ts.URL + "/stable.json", Key: EncodePublicKey(r.pub)}}}, catPriv)
	cats := newCatalogs(t)
	src := CatalogSource{URL: r.ts.URL + "/apps.json", Key: EncodePublicKey(catPub)}
	cat, err := cats.Preview(context.Background(), src)
	if err != nil || cat.Title != "Test apps" || len(cat.Apps) != 1 {
		t.Fatalf("preview: %+v, %v", cat, err)
	}
	if srcs, _ := cats.Sources(); len(srcs) != 0 {
		t.Fatalf("a preview added the store: %+v", srcs)
	}
	if _, err := os.Stat(cats.Cache); err == nil {
		t.Fatal("a preview kept a copy")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := cats.Preview(context.Background(), CatalogSource{URL: src.URL, Key: EncodePublicKey(other)}); err == nil {
		t.Fatal("a preview with the wrong key was accepted")
	}
}
