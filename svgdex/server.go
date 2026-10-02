package svgdex

import (
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/cors"
)

// ServerOptions: Origins are the browser origins allowed to call the API (empty: any — the catalogue is public and
// read-only, and no cookies are involved); Log, when set, hears one line per request.
type ServerOptions struct {
	Origins []string
	Log     func(format string, args ...any)
}

//go:embed page.html
var testPage []byte

// Handler serves a store read-only (GET / HEAD):
//
//	GET /search?q=mito&set=bio,ui&license=MIT&style=mono&limit=60&offset=0&facets=1   → Result (items get "url")
//	GET /suggest?q=mi&limit=10   → ["mitochondria", …]
//	GET /sets                    → [Set…] with counts
//	GET /icon/<id>               → one icon's details
//	GET /svg/<hash>.svg          → the SVG (immutable: cached a year; gzip when asked)
//	GET /                        → a small test page (search box + results)
//
// An item's "url" is relative to the service ("svg/<hash>.svg"): a client resolves it against the address it called.
func Handler(st *Store, opt ServerOptions) http.Handler {
	m := http.NewServeMux()
	open := len(opt.Origins) == 0
	m.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		q := Query{Q: v.Get("q"), License: v.Get("license"), Style: v.Get("style"), Facets: v.Get("facets") == "1" || v.Get("facets") == "true"}
		for _, s := range v["set"] {
			for _, x := range strings.Split(s, ",") {
				if x = strings.TrimSpace(x); x != "" {
					q.Sets = append(q.Sets, x)
				}
			}
		}
		q.Limit, _ = strconv.Atoi(v.Get("limit"))
		q.Offset, _ = strconv.Atoi(v.Get("offset"))
		res, err := st.Search(q)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, r, withURLs(res), 60)
	})
	m.HandleFunc("/suggest", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out, err := st.Suggest(r.URL.Query().Get("q"), n)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, r, out, 300)
	})
	m.HandleFunc("/sets", func(w http.ResponseWriter, r *http.Request) {
		out, err := st.Sets()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, r, out, 60)
	})
	m.HandleFunc("/icon/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/icon/"), 10, 64)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		ic, err := st.Get(id)
		if err != nil || ic == nil {
			jsonError(w, http.StatusNotFound, nil)
			return
		}
		writeJSON(w, r, itemOf(*ic), 300)
	})
	m.HandleFunc("/svg/", func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/svg/"), ".svg")
		if !ValidHash(hash) {
			http.NotFound(w, r)
			return
		}
		etag := `"` + hash[:16] + `"`
		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("Content-Type", "image/svg+xml")
		h.Set("X-Content-Type-Options", "nosniff")
		// Opened on its own (not as an <img>), an SVG is a document: nothing in it may run or load.
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		// Cached for a year, so the cached copy must serve every kind of request: an <img> asks without an Origin,
		// a fetch() with one — a response cached from the <img> without CORS headers would fail the fetch. Open:
		// always "*"; restricted: the answer depends on the Origin, so caches must keep them apart.
		if open {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Add("Vary", "Origin")
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		b, err := os.ReadFile(st.BlobPath(hash))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h.Add("Vary", "Accept-Encoding")
		if len(b) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			gz.Write(b)
			gz.Close()
			return
		}
		w.Write(b)
	})
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(testPage)
	})

	var h http.Handler = readOnly(m)
	c := cors.Options{AllowedMethods: []string{"GET", "HEAD"}, AllowedOrigins: []string{"*"}, MaxAge: 600}
	if len(opt.Origins) > 0 {
		c.AllowedOrigins = opt.Origins
	}
	h = cors.New(c).Handler(h)
	if opt.Log != nil {
		next := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := time.Now()
			rec := &status{ResponseWriter: w, code: 200}
			next.ServeHTTP(rec, r)
			opt.Log("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.code, time.Since(t).Round(time.Millisecond))
		})
	}
	return h
}

// Item is an icon as the API returns it: its details plus where its SVG is.
type Item struct {
	Icon
	URL string `json:"url"`
}

func itemOf(ic Icon) Item { return Item{Icon: ic, URL: "svg/" + ic.Hash + ".svg"} }

func withURLs(r *Result) any {
	items := make([]Item, len(r.Items))
	for i, ic := range r.Items {
		items[i] = itemOf(ic)
	}
	return struct {
		Total  int                       `json:"total"`
		Items  []Item                    `json:"items"`
		Next   int                       `json:"next,omitempty"`
		Facets map[string]map[string]int `json:"facets,omitempty"`
	}{r.Total, items, r.Next, r.Facets}
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any, maxAge int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(maxAge))
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func jsonError(w http.ResponseWriter, code int, err error) {
	msg := http.StatusText(code)
	if err != nil {
		msg = err.Error()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
