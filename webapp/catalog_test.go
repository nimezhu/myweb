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

// putCatalog publishes apps.json on the release server, signed with priv.
func (r *release) putCatalog(t *testing.T, c Catalog, priv ed25519.PrivateKey) {
	t.Helper()
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files["apps.json"] = data
	r.files["apps.json.sig"] = Sign(data, priv)
}

func newCatalogs(t *testing.T) *Catalogs {
	dir := t.TempDir()
	return &Catalogs{File: filepath.Join(dir, "catalogs.json"), Cache: filepath.Join(dir, "catalogs")}
}

func TestCatalog(t *testing.T) {
	r := newRelease(t)
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1"}, nil)
	catPub, catPriv, _ := ed25519.GenerateKey(rand.Reader)
	entry := CatalogApp{Name: "demo", Title: "Demo", Manifest: r.ts.URL + "/stable.json", Key: EncodePublicKey(r.pub)}
	r.putCatalog(t, Catalog{Title: "test", Apps: []CatalogApp{entry}}, catPriv)
	src := CatalogSource{URL: r.ts.URL + "/apps.json", Key: EncodePublicKey(catPub)}

	cats := newCatalogs(t)
	defer func(u, k string) { DefaultCatalogURL, DefaultCatalogKey = u, k }(DefaultCatalogURL, DefaultCatalogKey)
	DefaultCatalogURL, DefaultCatalogKey = src.URL, src.Key
	if srcs, _ := cats.Sources(); len(srcs) != 1 || srcs[0] != src {
		t.Fatalf("default sources: %v", srcs)
	}
	ctx := context.Background()
	a, err := cats.Find(ctx, "demo")
	if err != nil || a.Manifest != entry.Manifest || a.Catalog != src.URL {
		t.Fatalf("Find: %+v, %v", a, err)
	}
	if _, err := cats.Find(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "available: demo") {
		t.Fatalf("Find missing: %v", err)
	}

	// the entry installs like -web-from with its key
	in, err := (&Flags{From: a.Manifest, Key: a.Key}).Installer("0.1.0", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Update(ctx); err != nil || in.Current() != "1.0.0" {
		t.Fatalf("install from the catalog: %v %s", err, in.Current())
	}

	// a catalog changed on the server without the key is refused, even
	// when the cached copy is stale
	r.mu.Lock()
	r.files["apps.json"] = []byte(strings.Replace(string(r.files["apps.json"]), "Demo", "Evil", 1))
	r.mu.Unlock()
	if _, _, err := cats.Fetch(ctx, src, true); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered: %v", err)
	}
	// signed with another key
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	r.putCatalog(t, Catalog{Apps: []CatalogApp{entry}}, other)
	if _, _, err := cats.Fetch(ctx, src, true); err == nil {
		t.Fatal("a catalog signed with another key was accepted")
	}

	// offline: the cached copy
	r.putCatalog(t, Catalog{Title: "test", Apps: []CatalogApp{entry}}, catPriv)
	if _, _, err := cats.Fetch(ctx, src, true); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.down = true
	r.mu.Unlock()
	if cat, _, err := cats.Fetch(ctx, src, true); err != nil || len(cat.Apps) != 1 {
		t.Fatalf("offline: %v", err)
	}
	// a tampered cache is not trusted either
	files, _ := filepath.Glob(filepath.Join(cats.Cache, "*.json"))
	b, _ := os.ReadFile(files[0])
	os.WriteFile(files[0], []byte(strings.Replace(string(b), `"data":"`, `"data":"AAAA`, 1)), 0o644)
	if _, _, err := cats.Fetch(ctx, src, false); err == nil {
		t.Fatal("a tampered cache was accepted")
	}
}

func TestCatalogAddRemove(t *testing.T) {
	r := newRelease(t)
	catPub, catPriv, _ := ed25519.GenerateKey(rand.Reader)
	r.putCatalog(t, Catalog{Apps: []CatalogApp{{Name: "demo", Manifest: r.ts.URL + "/stable.json", Key: EncodePublicKey(r.pub)}}}, catPriv)
	cats := newCatalogs(t)
	defer func(u, k string) { DefaultCatalogURL, DefaultCatalogKey = u, k }(DefaultCatalogURL, DefaultCatalogKey)
	DefaultCatalogURL, DefaultCatalogKey = "https://example.invalid/apps.json", EncodePublicKey(catPub)
	ctx := context.Background()

	if _, err := cats.Add(ctx, CatalogSource{URL: r.ts.URL + "/apps.json", Key: EncodePublicKey(r.pub)}); err == nil {
		t.Fatal("added a catalog with the wrong key")
	}
	if _, err := cats.Add(ctx, CatalogSource{URL: r.ts.URL + "/apps.json", Key: EncodePublicKey(catPub)}); err != nil {
		t.Fatal(err)
	}
	srcs, _ := cats.Sources()
	if len(srcs) != 2 || srcs[1].URL != r.ts.URL+"/apps.json" {
		t.Fatalf("sources: %v", srcs)
	}
	apps, errs := cats.Apps(ctx, false)
	if len(apps) != 1 || len(errs) != 1 { // the default one can't be reached
		t.Fatalf("apps %v errs %v", apps, errs)
	}
	if err := cats.Remove(DefaultCatalogURL); err != nil {
		t.Fatal(err)
	}
	if apps, errs := cats.Apps(ctx, false); len(apps) != 1 || len(errs) != 0 {
		t.Fatalf("after removing the default: %v %v", apps, errs)
	}
	if err := cats.Remove("https://nope/apps.json"); err == nil {
		t.Fatal("removed a catalog that isn't there")
	}
}

func TestCatalogValidate(t *testing.T) {
	key := EncodePublicKey(make([]byte, ed25519.PublicKeySize))
	ok := CatalogApp{Name: "a", Manifest: "https://x/stable.json", Key: key}
	for name, c := range map[string]Catalog{
		"bad name":     {Apps: []CatalogApp{{Name: "../a", Manifest: ok.Manifest, Key: key}}},
		"twice":        {Apps: []CatalogApp{ok, ok}},
		"bad manifest": {Apps: []CatalogApp{{Name: "a", Manifest: "file:///etc/passwd", Key: key}}},
		"plain http":   {Apps: []CatalogApp{{Name: "a", Manifest: "http://example.com/stable.json", Key: key}}},
		"bad key":      {Apps: []CatalogApp{{Name: "a", Manifest: ok.Manifest, Key: "nope"}}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&Catalog{Apps: []CatalogApp{ok}}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestCatalogLinks(t *testing.T) {
	r := newRelease(t)
	catPub, catPriv, _ := ed25519.GenerateKey(rand.Reader)
	r.putCatalog(t, Catalog{Apps: []CatalogApp{{Name: "demo", Manifest: r.ts.URL + "/stable.json", Key: EncodePublicKey(r.pub),
		Icon: "img/demo.png", Screenshots: []string{"img/s1.png", "https://cdn.example/s2.png"}, Tags: []string{"graphics"}}}}, catPriv)
	cats := newCatalogs(t)
	cat, _, err := cats.Fetch(context.Background(), CatalogSource{URL: r.ts.URL + "/apps.json", Key: EncodePublicKey(catPub)}, true)
	if err != nil {
		t.Fatal(err)
	}
	a := cat.Apps[0]
	if a.Icon != r.ts.URL+"/img/demo.png" || a.Screenshots[0] != r.ts.URL+"/img/s1.png" || a.Screenshots[1] != "https://cdn.example/s2.png" {
		t.Fatalf("links: %+v", a)
	}
	key := EncodePublicKey(make([]byte, ed25519.PublicKeySize))
	for name, a := range map[string]CatalogApp{
		"javascript icon": {Icon: "javascript:alert(1)"},
		"data screenshot": {Screenshots: []string{"data:image/png;base64,AAAA"}},
		"bad tag":         {Tags: []string{"a,b"}},
		"empty tag":       {Tags: []string{""}},
	} {
		a.Name, a.Manifest, a.Key = "x", "https://x/stable.json", key
		if err := (&Catalog{Apps: []CatalogApp{a}}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
