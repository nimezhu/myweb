package svgdex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	st := newStore(t)
	put(t, st, Set{ID: "bio", Name: "Bioicons", License: "CC-BY-4.0"}, map[string][]byte{"cells/neuron.svg": svg("#000"), "lab/flask.svg": svg("#f00")}, nil)
	h := Handler(st, ServerOptions{})
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/search?q=neu&facets=1", "Origin", "https://app.example")
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("search: %d cors=%q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
	var res struct {
		Total int
		Items []struct {
			ID                       int64
			Name, Hash, URL, License string
		}
		Facets map[string]map[string]int
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Total != 1 || res.Items[0].Name != "neuron" || res.Items[0].URL != "svg/"+res.Items[0].Hash+".svg" || res.Items[0].License != "CC-BY-4.0" || res.Facets["set"]["bio"] != 1 {
		t.Fatalf("search body: %s", w.Body)
	}
	svgURL := "/" + res.Items[0].URL
	w = get(svgURL)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Fatalf("svg: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("an SVG carries CORS headers even when asked without an Origin (an <img>): its cached copy serves fetch() too")
	}
	if w2 := get(svgURL, "If-None-Match", w.Header().Get("ETag")); w2.Code != http.StatusNotModified {
		t.Errorf("etag: %d", w2.Code)
	}
	for _, p := range []string{"/svg/../index.db", "/svg/abc.svg", "/svg/%2e%2e%2findex.db", "/svg/" + strings.Repeat("g", 64) + ".svg"} {
		if w := get(p); w.Code == 200 {
			t.Errorf("only stored hashes are files: %s → %d", p, w.Code)
		}
	}
	if w := get("/sets"); !strings.Contains(w.Body.String(), `"count":2`) {
		t.Errorf("sets: %s", w.Body)
	}
	if w := get("/suggest?q=fl"); !strings.Contains(w.Body.String(), "flask") {
		t.Errorf("suggest: %s", w.Body)
	}
	if w := get("/icon/" + strings.TrimSpace(strings.Split(strings.Split(get("/search?q=flask").Body.String(), `"id":`)[1], ",")[0])); !strings.Contains(w.Body.String(), `"name":"flask"`) {
		t.Errorf("icon: %s", w.Body)
	}
	if w := get("/"); !strings.Contains(w.Body.String(), "svgdex") {
		t.Error("test page")
	}
	r := httptest.NewRequest("POST", "/search", nil)
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, r)
	if pw.Code != http.StatusMethodNotAllowed {
		t.Errorf("read-only: %d", pw.Code)
	}
}
