package webapp

// A catalog is a signed list of apps, so they can be installed by name
// (myweb app install NAME) instead of by manifest URL and key:
//
//	apps.json      {"title":"…","apps":[{"name":"rx-graphic","title":"…","description":"…",
//	                "manifest":"https://…/stable.json","key":"<the app's public key>"}]}
//	apps.json.sig  its signature, made with myweb-publish catalog sign
//
// myweb trusts a catalog through its key; catalogs are added with myweb app
// catalog add URL -key KEY (none is built in). An app installed from a
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

// CatalogApp is one app in a catalog.
type CatalogApp struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Manifest    string   `json:"manifest"`
	Key         string   `json:"key"`
	Homepage    string   `json:"homepage,omitempty"`
	Icon        string   `json:"icon,omitempty"`        // an image URL, square; may be relative to apps.json
	Screenshots []string `json:"screenshots,omitempty"` // image URLs; may be relative
	Tags        []string `json:"tags,omitempty"`        // e.g. "graphics", "editor"
	// Plugins is the app's plugin store, if it has one (a plugdex store's
	// …/hosts/<host>/index.json): Home lists the profiles its free channels
	// offer for the app, and opens the app as one (?app=ID&profile=URL).
	Plugins string `json:"plugins,omitempty"`
	Catalog string `json:"catalog,omitempty"` // set when loaded: the catalog's URL
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
		for _, l := range append([]string{a.Icon, a.Homepage}, a.Screenshots...) {
			if l != "" && !linkOK(l) {
				return fmt.Errorf("%s: %q is not an http(s) link", a.Name, l)
			}
		}
		if a.Plugins != "" && !linkOK(a.Plugins) {
			return fmt.Errorf("%s: plugins %q is not an http(s) link", a.Name, a.Plugins)
		}
		for _, t := range a.Tags {
			if t == "" || len(t) > 32 || strings.ContainsAny(t, ",<>\"\n") {
				return fmt.Errorf("%s: invalid tag %q", a.Name, t)
			}
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

// linkOK: an http(s) link, or one relative to the catalog (no other scheme).
func linkOK(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "" && u.Host == "" || (u.Scheme == "https" || u.Scheme == "http") && u.Host != "")
}

// webURL: https, or http for localhost only, as for -web-from.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && secureURL(u)
}

// CatalogSource is a catalog myweb reads, and the key that signs it.
type CatalogSource struct {
	URL  string `json:"url"`
	Key  string `json:"key"`
	Name string `json:"name,omitempty"` // a store link's name: what to call it until its catalog is read
}

// Catalogs are the catalogs of one myweb home: the list in File, the
// downloaded copies in Cache (checked with the server each time they are
// read, and used as they are when offline).
type Catalogs struct {
	File   string // e.g. ~/.myweb/catalogs.json
	Cache  string // e.g. ~/.myweb/catalogs/
	Client *http.Client
	MaxAge time.Duration // a cached copy younger than this is used without asking (0: always ask)
	Seeded string        // e.g. ~/.myweb/catalogs-seeded.json: the default stores added once (see Seed)
}

// Sources is the saved list (none until one is added).
func (c *Catalogs) Sources() ([]CatalogSource, error) {
	b, err := os.ReadFile(c.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
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

// Preview reads a catalog without adding it (nor keeping a copy): what a
// store link leads to, to show before it is added. Its signature is checked.
func (c *Catalogs) Preview(ctx context.Context, src CatalogSource) (*Catalog, error) {
	if !webURL(src.URL) {
		return nil, fmt.Errorf("a catalog must be an https URL (http only for localhost): %s", src.URL)
	}
	key, err := ParsePublicKey(src.Key)
	if err != nil {
		return nil, err
	}
	got, err := c.download(ctx, src.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	return parseCatalog(src, got.Data, got.Sig, key)
}

// Remove drops a catalog by URL.
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
	Fetched      time.Time `json:"fetched"` // when the server last said this is current
	Data         []byte    `json:"data"`    // apps.json as signed
	Sig          []byte    `json:"sig"`
	ETag         string    `json:"etag,omitempty"` // apps.json's validators, to ask if it changed
	LastModified string    `json:"lastModified,omitempty"`
}

func (c *Catalogs) cacheFile(u string) string {
	sum := sha256.Sum256([]byte(u))
	return filepath.Join(c.Cache, hex.EncodeToString(sum[:6])+".json")
}

// Fetch reads one catalog. It asks the server whether apps.json changed
// since the cached copy (a conditional GET: an unchanged catalog costs one
// small request) and downloads it when it did; refresh downloads it anyway,
// and MaxAge skips asking while the copy is that young. Offline, the cached
// copy of any age. Either way the signature is checked. It returns when the
// copy was last known current.
func (c *Catalogs) Fetch(ctx context.Context, src CatalogSource, refresh bool) (*Catalog, time.Time, error) {
	if !webURL(src.URL) {
		return nil, time.Time{}, fmt.Errorf("catalog %s: must be https (http only for localhost)", src.URL)
	}
	key, err := ParsePublicKey(src.Key)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("catalog %s: key: %v", src.URL, err)
	}
	var cached *cachedCatalog
	if b, err := os.ReadFile(c.cacheFile(src.URL)); err == nil {
		var cc cachedCatalog
		if json.Unmarshal(b, &cc) == nil {
			cached = &cc
		}
	}
	var cat *Catalog
	if cached != nil {
		if cat, err = parseCatalog(src, cached.Data, cached.Sig, key); err != nil {
			cached, cat = nil, nil // not to be trusted, nor asked about: download it
		}
	}
	if cached != nil && !refresh && c.MaxAge > 0 && time.Since(cached.Fetched) < c.MaxAge {
		return cat, cached.Fetched, nil
	}
	var since *cachedCatalog
	if !refresh {
		since = cached
	}
	got, err := c.download(ctx, src.URL, since)
	if err == nil && got == nil { // not modified
		cached.Fetched = time.Now().UTC()
		c.saveCache(src.URL, cached)
		return cat, cached.Fetched, nil
	}
	if err != nil {
		if cached != nil { // offline: what we had
			return cat, cached.Fetched, nil
		}
		return nil, time.Time{}, fmt.Errorf("catalog %s: %v", src.URL, err)
	}
	if cat, err = parseCatalog(src, got.Data, got.Sig, key); err != nil {
		return nil, time.Time{}, err
	}
	got.Fetched = time.Now().UTC()
	c.saveCache(src.URL, got)
	return cat, got.Fetched, nil
}

func (c *Catalogs) saveCache(u string, cc *cachedCatalog) {
	if b, err := json.Marshal(cc); err == nil && os.MkdirAll(c.Cache, 0o755) == nil {
		os.WriteFile(c.cacheFile(u), b, 0o644)
	}
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
	base, _ := url.Parse(src.URL)
	abs := func(s string) string { // links relative to apps.json
		if u, err := url.Parse(s); err == nil && s != "" {
			return base.ResolveReference(u).String()
		}
		return s
	}
	for i := range cat.Apps {
		a := &cat.Apps[i]
		a.Catalog = src.URL
		a.Icon, a.Homepage, a.Plugins = abs(a.Icon), abs(a.Homepage), abs(a.Plugins)
		for j := range a.Screenshots {
			a.Screenshots[j] = abs(a.Screenshots[j])
		}
	}
	return &cat, nil
}

// download gets apps.json and its signature — nil, nil when since (a cached
// copy) is still what the server has.
func (c *Catalogs) download(ctx context.Context, u string, since *cachedCatalog) (*cachedCatalog, error) {
	got := &cachedCatalog{}
	resp, err := c.get(ctx, u, since)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	got.ETag, got.LastModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if got.Data, err = readBody(resp); err != nil {
		return nil, err
	}
	sresp, err := c.get(ctx, u+".sig", nil)
	if err == nil {
		got.Sig, err = readBody(sresp)
	}
	if err != nil {
		return nil, fmt.Errorf("signature: %v", err)
	}
	return got, nil
}

// get is a GET of u, conditional on since's validators; nil, nil when the
// server answers 304 Not Modified. The caller reads (and closes) the body.
func (c *Catalogs) get(ctx context.Context, u string, since *cachedCatalog) (*http.Response, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if since != nil {
		if since.ETag != "" {
			req.Header.Set("If-None-Match", since.ETag)
		}
		if since.LastModified != "" {
			req.Header.Set("If-Modified-Since", since.LastModified)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if since != nil && (since.ETag != "" || since.LastModified != "") && resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return resp, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
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
		if srcs, _ := c.Sources(); len(srcs) == 0 {
			return CatalogApp{}, fmt.Errorf("%s: no catalogs to look in; add one with: myweb app catalog add URL -key KEY", name)
		}
		return CatalogApp{}, fmt.Errorf("%s is in no catalog (no catalog lists any app)", name)
	}
	return CatalogApp{}, fmt.Errorf("%s is in no catalog; available: %s", name, strings.Join(names, ", "))
}
