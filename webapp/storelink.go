package webapp

// A store link is a catalog and its key in one string, so a store is added by
// pasting a link (or, later, clicking one) instead of typing a URL and a key:
//
//	https://store.example/io/apps.json#key=<base64 key>&name=Example%20apps
//	myweb://add-store?url=https://store.example/io/apps.json&key=…&name=…
//
// The key still checks the catalog's signature, as when it is typed; the
// name is only what to call the store until its catalog has been read.
//
// A release build may add stores of its own to a new home, once
// (DefaultStores, and a stores.txt beside the program); the open-source build
// has none.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// StoreLink is a parsed store link.
type StoreLink struct {
	URL  string // the catalog: …/apps.json
	Key  string // its public key, base64
	Name string // what to call it until the catalog's title is known
}

// ParseStoreLink reads either form of a store link.
func ParseStoreLink(s string) (StoreLink, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || s == "" {
		return StoreLink{}, errors.New("that isn't a store link")
	}
	var q url.Values
	var l StoreLink
	if strings.EqualFold(u.Scheme, "myweb") { // myweb://add-store?url=…&key=…
		q = u.Query()
		l.URL = q.Get("url")
	} else {
		if q, err = url.ParseQuery(u.EscapedFragment()); err != nil {
			return StoreLink{}, errors.New("that isn't a store link")
		}
		u.Fragment, u.RawFragment = "", ""
		l.URL = u.String()
	}
	// a + in the key may have come through unescaped, and read as a space
	l.Key, l.Name = strings.ReplaceAll(q.Get("key"), " ", "+"), strings.TrimSpace(q.Get("name"))
	if !webURL(l.URL) {
		return StoreLink{}, errors.New("a store link leads to an https address (http only for localhost)")
	}
	if l.Key == "" {
		return StoreLink{}, errors.New("this link has no key: a store link ends with #key=…")
	}
	key, err := ParsePublicKey(l.Key)
	if err != nil {
		return StoreLink{}, errors.New("this store link is damaged: copy the whole link again")
	}
	l.Key = EncodePublicKey(key)
	return l, nil
}

// String is the link in its https form.
func (l StoreLink) String() string {
	q := url.Values{"key": {l.Key}}
	if l.Name != "" {
		q.Set("name", l.Name)
	}
	return l.URL + "#" + q.Encode()
}

// Source is the catalog the link names.
func (l StoreLink) Source() CatalogSource { return CatalogSource{URL: l.URL, Key: l.Key, Name: l.Name} }

// LinkOf is the store link of a catalog myweb reads.
func LinkOf(src CatalogSource) StoreLink {
	return StoreLink{URL: src.URL, Key: src.Key, Name: src.Name}
}

// KeyFingerprint is a short, readable digest of a public key, to compare with
// what the store's website shows: e.g. "3F2A 9C1B 77E0".
func KeyFingerprint(key string) string {
	k, err := ParsePublicKey(key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(k)
	h := strings.ToUpper(hex.EncodeToString(sum[:6]))
	return h[:4] + " " + h[4:8] + " " + h[8:]
}

// DefaultStores are the store links a release build adds to a new home,
// separated by spaces or new lines — set when building:
//
//	go build -ldflags "-X github.com/nimezhu/myweb/webapp.DefaultStores=<link> …"
var DefaultStores string

// SplitStoreLinks is the links in text: one per line or space-separated;
// blank lines and # comments are skipped.
func SplitStoreLinks(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Fields(line)...)
	}
	return out
}

// Seed adds each store of links that this home has never had added this way,
// once: a store the user removes doesn't come back. It doesn't read the
// stores (that happens when their apps are listed), so it works offline.
func (c *Catalogs) Seed(links []string) error {
	if len(links) == 0 || c.Seeded == "" {
		return nil
	}
	var seeded []string
	if b, err := os.ReadFile(c.Seeded); err == nil {
		json.Unmarshal(b, &seeded)
	}
	done := map[string]bool{}
	for _, u := range seeded {
		done[u] = true
	}
	list, err := c.Sources()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, s := range list {
		have[s.URL] = true
	}
	added := false
	var errs []error
	for _, s := range links {
		l, err := ParseStoreLink(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("default store %s: %v", s, err))
			continue
		}
		if done[l.URL] {
			continue
		}
		if !have[l.URL] {
			list = append(list, l.Source())
			have[l.URL] = true
		}
		seeded = append(seeded, l.URL)
		done[l.URL] = true
		added = true
	}
	if added {
		if err := c.save(list); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(seeded, "", "  ")
		if err := os.WriteFile(c.Seeded, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}
