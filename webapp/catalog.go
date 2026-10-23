package webapp

// A catalog is a signed list of apps, so they can be installed by name
// (myweb app install NAME) instead of by manifest URL and key:
//
//	apps.json      {"title":"…","apps":[{"name":"rx-graphic","title":"…","description":"…",
//	                "manifest":"https://…/stable.json","key":"<the app's public key>"}]}
//	apps.json.sig  its signature, made with myweb-publish catalog sign
//
// myweb trusts a catalog through its key: the default one is built in, others
// are added with myweb app catalog add URL -key KEY. An app installed from a
// catalog keeps the key it was installed with; a later change to the catalog
// does not change which key its updates must be signed with.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCatalogURL and DefaultCatalogKey name the catalog myweb starts with.
var (
	DefaultCatalogURL = ""
	DefaultCatalogKey = ""
)

// CatalogApp is one app in a catalog.
type CatalogApp struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Manifest    string `json:"manifest"`
	Key         string `json:"key"`
	Homepage    string `json:"homepage,omitempty"`
	Catalog     string `json:"catalog,omitempty"` // set when loaded: the catalog's URL
}

// Catalog is the content of apps.json.
type Catalog struct {
	Title string       `json:"title,omitempty"`
	Apps  []CatalogApp `json:"apps"`
}

// Validate checks every entry: a valid unique name, a manifest URL and a key.
func (c *Catalog) Validate() error {
	seen := map[string]bool{}
	for i, a := range c.Apps {
		switch {
		case !ValidName(a.Name):
			return fmt.Errorf("app %d: invalid name %q", i+1, a.Name)
		case seen[a.Name]:
			return fmt.Errorf("%s is listed twice", a.Name)
		case !webURL(a.Manifest):
			return fmt.Errorf("%s: manifest must be an https URL (http only for localhost), not %q", a.Name, a.Manifest)
		}
		if _, err := ParsePublicKey(a.Key); err != nil {
			return fmt.Errorf("%s: key: %v", a.Name, err)
		}
		seen[a.Name] = true
	}
	return nil
}

// Encode is the catalog as it is published and signed.
func (c *Catalog) Encode() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	return append(b, '\n'), err
}

// webURL: https, or http for localhost only, as for -web-from.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && secureURL(u)
}

// CatalogSource is a catalog myweb reads, and the key that signs it.
type CatalogSource struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// Catalogs are the catalogs of one myweb home: the list in File, the
// downloaded copies in Cache (used for an hour, and when offline).
type Catalogs struct {
	File   string // e.g. ~/.myweb/catalogs.json
	Cache  string // e.g. ~/.myweb/catalogs/
	Client *http.Client
	MaxAge time.Duration
}

// Sources is the saved list; without one, the default catalog.
func (c *Catalogs) Sources() ([]CatalogSource, error) {
	b, err := os.ReadFile(c.File)
	if errors.Is(err, os.ErrNotExist) {
		if DefaultCatalogURL == "" || DefaultCatalogKey == "" {
			return nil, nil
		}
		return []CatalogSource{{DefaultCatalogURL, DefaultCatalogKey}}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []CatalogSource
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %v", c.File, err)
	}
	return list, nil
}

// Add adds or replaces (by URL) a catalog after checking it can be read.
func (c *Catalogs) Add(ctx context.Context, src CatalogSource) (*Catalog, error) {
	if !webURL(src.URL) {
		return nil, fmt.Errorf("a catalog must be an https URL (http only for localhost): %s", src.URL)
	}
	key, err := LoadPublicKey(src.Key)
	if err != nil {
		return nil, err
	}
	src.Key = EncodePublicKey(key)
	cat, _, err := c.Fetch(ctx, src, true)
	if err != nil {
		return nil, err
	}
	list, err := c.Sources()
	if err != nil {
		return nil, err
	}
	out := []CatalogSource{}
	for _, s := range list {
		if s.URL != src.URL {
			out = append(out, s)
		}
	}
	return cat, c.save(append(out, src))
}

// Remove drops a catalog by URL (the default one too).
func (c *Catalogs) Remove(u string) error {
	list, err := c.Sources()
	if err != nil {
		return err
	}
	out := []CatalogSource{}
	for _, s := range list {
		if s.URL != u {
			out = append(out, s)
		}
	}
	if len(out) == len(list) {
		return fmt.Errorf("no catalog %s", u)
	}
	os.Remove(c.cacheFile(u))
	return c.save(out)
}

func (c *Catalogs) save(list []CatalogSource) error {
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(c.File), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.File, append(b, '\n'), 0o644)
}

type cachedCatalog struct {
	Fetched time.Time `json:"fetched"`
	Data    []byte    `json:"data"` // apps.json as signed
	Sig     []byte    `json:"sig"`
}

func (c *Catalogs) cacheFile(u string) string {
	sum := sha256.Sum256([]byte(u))
	return filepath.Join(c.Cache, hex.EncodeToString(sum[:6])+".json")
}

// Fetch reads one catalog: the cached copy while it is fresh (unless
// refresh), else from its URL; offline, the cached copy of any age. Either
// way the signature is checked. It returns when the copy was fetched.
func (c *Catalogs) Fetch(ctx context.Context, src CatalogSource, refresh bool) (*Catalog, time.Time, error) {
	if !webURL(src.URL) {
		return nil, time.Time{}, fmt.Errorf("catalog %s: must be https (http only for localhost)", src.URL)
	}
	key, err := ParsePublicKey(src.Key)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("catalog %s: key: %v", src.URL, err)
	}
	maxAge := c.MaxAge
	if maxAge == 0 {
		maxAge = time.Hour
	}
	var cached *cachedCatalog
	if b, err := os.ReadFile(c.cacheFile(src.URL)); err == nil {
		var cc cachedCatalog
		if json.Unmarshal(b, &cc) == nil {
			cached = &cc
		}
	}
	if cached != nil && !refresh && time.Since(cached.Fetched) < maxAge {
		if cat, err := parseCatalog(src, cached.Data, cached.Sig, key); err == nil {
			return cat, cached.Fetched, nil
		}
	}
	data, sig, err := c.download(ctx, src.URL)
	if err != nil {
		if cached != nil { // offline: what we had
			if cat, err2 := parseCatalog(src, cached.Data, cached.Sig, key); err2 == nil {
				return cat, cached.Fetched, nil
			}
		}
		return nil, time.Time{}, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	cat, err := parseCatalog(src, data, sig, key)
	if err != nil {
		return nil, time.Time{}, err
	}
	now := time.Now().UTC()
	if b, err := json.Marshal(cachedCatalog{now, data, sig}); err == nil && os.MkdirAll(c.Cache, 0o755) == nil {
		os.WriteFile(c.cacheFile(src.URL), b, 0o644)
	}
	return cat, now, nil
}

func parseCatalog(src CatalogSource, data, sig []byte, key ed25519.PublicKey) (*Catalog, error) {
	if _, err := Verify(data, sig, key); err != nil {
		return nil, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	var cat Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	if err := cat.Validate(); err != nil {
		return nil, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	for i := range cat.Apps {
		cat.Apps[i].Catalog = src.URL
	}
	return &cat, nil
}

func (c *Catalogs) download(ctx context.Context, u string) (data, sig []byte, err error) {
	if data, err = c.get(ctx, u); err != nil {
		return nil, nil, err
	}
	if sig, err = c.get(ctx, u+".sig"); err != nil {
		return nil, nil, fmt.Errorf("signature: %v", err)
	}
	return data, sig, nil
}

func (c *Catalogs) get(ctx context.Context, u string) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err == nil && len(b) > 4<<20 {
		err = errors.New("larger than 4 MiB")
	}
	return b, err
}

// Apps lists the apps of every catalog. A name in two catalogs is taken
// from the first; a catalog that can't be read is reported in errs.
func (c *Catalogs) Apps(ctx context.Context, refresh bool) (apps []CatalogApp, errs []error) {
	srcs, err := c.Sources()
	if err != nil {
		return nil, []error{err}
	}
	seen := map[string]bool{}
	for _, src := range srcs {
		cat, _, err := c.Fetch(ctx, src, refresh)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, a := range cat.Apps {
			if !seen[a.Name] {
				seen[a.Name] = true
				apps = append(apps, a)
			}
		}
	}
	return apps, errs
}

// Find is the catalog entry for an app name.
func (c *Catalogs) Find(ctx context.Context, name string) (CatalogApp, error) {
	apps, errs := c.Apps(ctx, false)
	for _, a := range apps {
		if a.Name == name {
			return a, nil
		}
	}
	if len(errs) > 0 {
		return CatalogApp{}, fmt.Errorf("%s is in no catalog that could be read (%v)", name, errs[0])
	}
	var names []string
	for _, a := range apps {
		names = append(names, a.Name)
	}
	if len(names) == 0 {
		return CatalogApp{}, fmt.Errorf("%s is in no catalog (no catalog lists any app)", name)
	}
	return CatalogApp{}, fmt.Errorf("%s is in no catalog; available: %s", name, strings.Join(names, ", "))
}
