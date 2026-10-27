package cli

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// storeRow is one store (a catalog this myweb reads) as Home shows it.
type storeRow struct {
	URL         string     `json:"url"`
	Site        string     `json:"site"`            // its host: what Home names it by when it has no title
	Title       string     `json:"title,omitempty"` // the catalog's own, once read
	Name        string     `json:"name,omitempty"`  // its store link's
	Apps        int        `json:"apps"`
	Checked     *time.Time `json:"checked,omitempty"` // when it was last known current
	Error       string     `json:"error,omitempty"`
	Fingerprint string     `json:"fingerprint"` // of its key, to compare with the store's website
	Link        string     `json:"link"`        // its store link, to share
}

// storePreview is what a store link leads to, before it is added.
type storePreview struct {
	storeRow
	Added    bool         `json:"added"` // already read here
	AppNames []storeEntry `json:"appList"`
}

type storeEntry struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

func storeRowOf(src webapp.CatalogSource, cat *webapp.Catalog, checked time.Time, err error) storeRow {
	r := storeRow{URL: src.URL, Name: src.Name, Fingerprint: webapp.KeyFingerprint(src.Key), Link: webapp.LinkOf(src).String()}
	if u, e := url.Parse(src.URL); e == nil {
		r.Site = u.Host
	}
	if err != nil {
		r.Error = err.Error()
	}
	if cat != nil {
		r.Title, r.Apps = cat.Title, len(cat.Apps)
	}
	if !checked.IsZero() {
		r.Checked = &checked
	}
	return r
}

// registerStoresCmd serves the stores at POST /cmd/stores, for Home: {} lists
// them; {"preview":LINK} reads what a store link leads to without adding it;
// {"add":LINK} adds it; {"remove":URL} drops one (its installed apps stay).
func registerStoresCmd(srv *myserver.Server, set *appSet) {
	srv.RegisterCmd("stores", func(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
		if req.User.IsApp() {
			return nil, myserver.Errorf(http.StatusForbidden, "apps can't manage stores")
		}
		var p struct {
			Preview string `json:"preview"`
			Add     string `json:"add"`
			Remove  string `json:"remove"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if p.Preview != "" || p.Add != "" || p.Remove != "" {
			if req.User != nil && req.User.Role != "admin" {
				return nil, myserver.Errorf(http.StatusForbidden, "only admins can change the stores")
			}
			// browsers let any site send a "simple" POST here: stores are
			// changed (and links read) only from this server's own pages
			if o := req.HTTP.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != req.HTTP.Host {
					return nil, myserver.Errorf(http.StatusForbidden, "stores can be changed only from this server's own pages")
				}
			}
		}
		cats := catalogsFor(set.dirs)
		switch {
		case p.Preview != "":
			l, err := webapp.ParseStoreLink(p.Preview)
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadRequest, "%v", err)
			}
			cat, err := cats.Preview(ctx, l.Source())
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadGateway, "this store can't be read: %v", err)
			}
			out := storePreview{storeRow: storeRowOf(l.Source(), cat, time.Now().UTC(), nil), AppNames: []storeEntry{}}
			for _, a := range cat.Apps {
				out.AppNames = append(out.AppNames, storeEntry{a.Name, a.Title, a.Icon})
			}
			if srcs, err := cats.Sources(); err == nil {
				for _, s := range srcs {
					out.Added = out.Added || s.URL == l.URL
				}
			}
			return out, nil
		case p.Add != "":
			l, err := webapp.ParseStoreLink(p.Add)
			if err != nil {
				return nil, myserver.Errorf(http.StatusBadRequest, "%v", err)
			}
			if _, err := cats.Add(ctx, l.Source()); err != nil {
				return nil, myserver.Errorf(http.StatusBadGateway, "%v", err)
			}
		case p.Remove != "":
			if err := cats.Remove(p.Remove); err != nil {
				return nil, myserver.Errorf(http.StatusNotFound, "%v", err)
			}
		}
		srcs, err := cats.Sources()
		if err != nil {
			return nil, err
		}
		out := []storeRow{}
		for _, s := range srcs {
			cat, checked, err := cats.Fetch(ctx, s, false)
			out = append(out, storeRowOf(s, cat, checked, err))
		}
		return out, nil
	}, myserver.Describe(`the stores (app catalogs) this myweb reads: {} lists them; {"preview":LINK} reads a store link without adding it; {"add":LINK} adds it; {"remove":URL} drops one (its installed apps stay)`))
}
