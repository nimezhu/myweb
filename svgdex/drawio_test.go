package svgdex

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLib(t *testing.T, dir, name string, entries []map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(entries)
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte("<mxlibrary>"+string(b)+"</mxlibrary>"), 0o644)
	return p
}

func TestImportDrawio(t *testing.T) {
	dir := t.TempDir()
	b64 := func(s string) string {
		return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(s))
	}
	a := writeLib(t, dir, "Bioicons-Cell_culture.xml", []map[string]any{
		{"title": "96_well_plate | CC0 Marcel Tisch", "data": b64(`<svg viewBox="0 0 10 10"><rect width="10" height="10" fill="#abc"/></svg>`)},
		{"title": "petri dish | CC-BY 3.0 Unported Servier", "data": "data:image/svg+xml," + url.PathEscape(`<svg width="8" height="8"><circle r="4"/></svg>`)},
		{"title": "petri dish | CC-BY 4.0 Unported DBCLS", "data": b64(`<svg viewBox="0 0 1 1"><path d="M0 0"/></svg>`)},
		{"title": "broken | CC0 X", "data": b64(`<html/>`)},
		{"title": "a shape", "xml": "dZDBDoIwDIafZneCv"},
	})
	b := writeLib(t, dir, "Bioicons-Oncology.xml", []map[string]any{
		{"title": "angiogenesis | MIT Someone", "data": "data:image/png;base64,iVBORw0KGgo=", "w": 30, "h": 20},
		{"data": b64(`<svg viewBox="0 0 4 4"><g id="tumour_cell"><path id="path12" d="M0 0"/></g></svg>`)},
	})
	st := newStore(t)
	rep, err := st.ImportDrawio([]string{a, b}, Set{ID: "bioicons", Name: "Bioicons", URL: "https://bioicons.com"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 5 || rep.Unsupported != 1 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "broken") {
		t.Fatalf("report: %+v", rep)
	}
	r, _ := st.Search(Query{Q: "well plate"})
	if len(r.Items) != 1 || r.Items[0].Name != "96 well plate" || r.Items[0].License != "CC0-1.0" || r.Items[0].Author != "Marcel Tisch" || r.Items[0].Key != "cell-culture/96-well-plate.svg" {
		t.Fatalf("title → name / licence / author / key: %+v", r.Items)
	}
	r, _ = st.Search(Query{Q: "petri"})
	lic := map[string]bool{}
	for _, i := range r.Items {
		lic[i.License] = true
	}
	if r.Total != 2 || !lic["CC-BY-3.0"] || !lic["CC-BY-4.0"] {
		t.Errorf("same name twice: both kept, licences parsed: %+v", r.Items)
	}
	if r, _ = st.Search(Query{Q: "cell culture"}); r.Total != 3 {
		t.Errorf("the file's category is a word: %d", r.Total)
	}
	r, _ = st.Search(Query{Q: "angio"})
	if r.Total != 1 || r.Items[0].Mono || !strings.Contains(strings.Join(r.Items[0].Tags, " "), "raster") || r.Items[0].W != 30 {
		t.Errorf("a PNG is wrapped, tagged raster, not mono: %+v", r.Items)
	}
	if r, _ = st.Search(Query{Q: "tumour"}); r.Total != 1 || r.Items[0].Name != "Oncology 2" {
		t.Errorf("an untitled entry: named by category + n, found by a meaningful id: %+v", r.Items)
	}
	// one file per set (no -set): ids and names from the file names
	rep, _ = st.ImportDrawio([]string{b}, Set{})
	if rep.Set != "bioicons-oncology" {
		t.Errorf("set id from the file: %s", rep.Set)
	}
	// a file with only draw.io shapes leaves no set
	c := writeLib(t, dir, "only-shapes.xml", []map[string]any{{"title": "s", "xml": "abc"}, {"title": "link", "data": "https://x/y.png"}})
	rep, err = st.ImportDrawio([]string{c}, Set{})
	sets, _ := st.Sets()
	for _, s := range sets {
		if s.ID == "only-shapes" {
			t.Error("an empty import leaves no set")
		}
		if s.ID == "bioicons-oncology" && s.Name != "Bioicons Oncology" {
			t.Errorf("pretty name: %q", s.Name)
		}
	}
	if err != nil || rep.Added != 0 || rep.Unsupported != 2 {
		t.Errorf("shapes + links are unsupported: %+v %v", rep, err)
	}
}

func TestParseDrawioTitle(t *testing.T) {
	for _, c := range []struct{ in, name, lic, author string }{
		{"Mosquito_gender | CC0 Mariana RuizVillareal", "Mosquito gender", "CC0-1.0", "Mariana RuizVillareal"},
		{"x | CC-BY 4.0 Unported SwissBioPics", "x", "CC-BY-4.0", "SwissBioPics"},
		{"x | CC-BY-SA 4.0 Someone", "x", "CC-BY-SA-4.0", "Someone"},
		{"PyTorch | BSD Facebook", "PyTorch", "BSD", "Facebook"},
		{"x | Some Person", "x", "", "Some Person"},
		{"plain title", "plain title", "", ""},
	} {
		n, l, a := parseDrawioTitle(c.in)
		if n != c.name || l != c.lic || a != c.author {
			t.Errorf("%q → %q %q %q", c.in, n, l, a)
		}
	}
}
