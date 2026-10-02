package svgdex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// draw.io libraries: an <mxlibrary> holding a JSON array of entries
//
//	{ "title": "Mosquito_gender | CC0 Mariana RuizVillareal", "data": "data:image/svg+xml;base64,…", "w": 40, "h": 40 }
//
// "data" is an embedded image (SVG — base64, utf8 or %-encoded — or PNG / JPEG); "xml" (instead of "data") is a
// compressed draw.io shape, which is not an SVG (counted as unsupported, not imported). Bioicons-style titles carry the
// licence and author after a "|".

type drawioEntry struct {
	Title string  `json:"title"`
	Data  string  `json:"data"`
	XML   string  `json:"xml"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// ImportDrawio (re)imports draw.io library files as ONE set: each file's name (without the set's name in front —
// "Bioicons-Cell_culture.xml" in the set "Bioicons" → "Cell culture") is its icons' category: a word to search by and
// the first part of their keys. SVG entries are imported as they are; a raster image (PNG / JPEG) is wrapped in an SVG
// and tagged "raster"; draw.io shapes are counted in the report's Unsupported. An entry without a title is named
// "<category> <n>" — searchable by its category and any meaningful ids inside it.
func (s *Store) ImportDrawio(files []string, set Set) (*ImportReport, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("svgdex: no draw.io files")
	}
	if set.ID == "" {
		set.ID = SetID(drawioBase(files[0]))
	}
	if set.Name == "" {
		set.Name = prettyName(drawioBase(files[0]))
	}
	var abs []string
	for _, f := range files {
		a, err := filepath.Abs(f)
		if err != nil {
			return nil, err
		}
		abs = append(abs, a)
	}
	set.Source = strings.Join(abs, ", ")
	if len(set.Source) > 500 {
		set.Source = filepath.Dir(abs[0]) + fmt.Sprintf(" (%d files)", len(abs))
	}
	batch, err := s.BeginSet(set)
	if err != nil {
		return nil, err
	}
	rep := &ImportReport{Set: set.ID}
	for _, f := range abs {
		entries, err := readDrawio(f)
		if err != nil {
			batch.Rollback()
			return nil, err
		}
		category := drawioCategory(drawioBase(f), set.Name)
		used := map[string]int{}
		for i, e := range entries {
			if !strings.HasPrefix(e.Data, "data:") { // a draw.io shape, or an image by link: not importable
				if e.XML != "" || e.Data != "" {
					rep.Unsupported++
				}
				continue
			}
			name, license, author := parseDrawioTitle(e.Title)
			svg, raster, err := drawioSVG(e)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s #%d %s: %v", filepath.Base(f), i+1, name, err))
				continue
			}
			ic := Icon{Name: name, License: license, Author: author, Tags: []string{category}}
			if ic.Name == "" {
				ic.Name = fmt.Sprintf("%s %d", category, i+1)
				ic.Tags = append(ic.Tags, meaningfulIDs(svg)...)
			}
			if raster {
				ic.Tags = append(ic.Tags, "raster")
			}
			slug := strings.Join(Words(ic.Name), "-")
			if slug == "" {
				slug = fmt.Sprint(i + 1)
			}
			if used[slug]++; used[slug] > 1 {
				slug = fmt.Sprintf("%s-%d", slug, used[slug])
			}
			ic.Key = strings.Join(Words(category), "-") + "/" + slug + ".svg"
			if _, err := batch.Put(ic, svg); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s #%d %s: %v", filepath.Base(f), i+1, ic.Name, err))
			}
		}
	}
	rep.Added = batch.Count()
	if rep.Added == 0 { // nothing usable (only draw.io shapes): no empty set
		return rep, batch.Rollback()
	}
	return rep, batch.Commit()
}

// prettyName: "material-design-icons" → "Material design icons".
func prettyName(s string) string {
	s = strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(s)), " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func drawioBase(f string) string { return strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)) }

// drawioCategory: the file's name without the set's name in front, separators as spaces.
func drawioCategory(base, setName string) string {
	b := base
	for _, p := range []string{setName, strings.ReplaceAll(setName, " ", "_"), strings.ReplaceAll(setName, " ", "-")} {
		if p != "" && len(b) > len(p) && strings.EqualFold(b[:len(p)], p) {
			b = strings.TrimLeft(b[len(p):], "-_ ")
			break
		}
	}
	return strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(b)), " ")
}

func readDrawio(f string) ([]drawioEntry, error) {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	start, end := bytes.Index(b, []byte("<mxlibrary>")), bytes.LastIndex(b, []byte("</mxlibrary>"))
	if start < 0 || end < start {
		return nil, fmt.Errorf("svgdex: %s: not a draw.io library (no <mxlibrary>)", filepath.Base(f))
	}
	var list []drawioEntry
	if err := json.Unmarshal(b[start+len("<mxlibrary>"):end], &list); err != nil {
		return nil, fmt.Errorf("svgdex: %s: %w", filepath.Base(f), err)
	}
	return list, nil
}

// "Mosquito_gender | CC0 Mariana RuizVillareal" → "Mosquito gender", "CC0-1.0", "Mariana RuizVillareal".
var titleLicense = regexp.MustCompile(`(?i)^(CC0|CC[- ]BY(?:[- ](?:SA|NC|ND|NC-SA|NC-ND))?(?:\s+\d\.\d)?(?:\s+(?:Unported|International|IGO))?|MIT|BSD(?:[- ]\d-Clause)?|Apache(?:[- ]2(?:\.0)?)?|GPL[- ]?\S*|Public Domain|PD)\b\s*(.*)$`)

func parseDrawioTitle(t string) (name, license, author string) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", "", ""
	}
	parts := strings.SplitN(t, "|", 2)
	name = strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(parts[0]))), " ")
	if len(parts) == 2 {
		rest := strings.TrimSpace(parts[1])
		if m := titleLicense.FindStringSubmatch(rest); m != nil {
			license, author = spdx(m[1]), strings.TrimSpace(m[2])
		} else {
			author = rest
		}
	}
	return
}

// spdx: a licence as written in a title → its SPDX-style id.
func spdx(l string) string {
	u := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(l, "-", " ")), " "))
	u = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(u, " UNPORTED"), " INTERNATIONAL"), " IGO"))
	switch {
	case u == "CC0":
		return "CC0-1.0"
	case u == "PUBLIC DOMAIN" || u == "PD":
		return "Public-Domain"
	case strings.HasPrefix(u, "CC BY"):
		parts := strings.Fields(strings.TrimPrefix(u, "CC "))
		ver := ""
		if len(parts) > 0 && regexp.MustCompile(`^\d\.\d$`).MatchString(parts[len(parts)-1]) {
			ver, parts = parts[len(parts)-1], parts[:len(parts)-1]
		}
		id := "CC-" + strings.Join(parts, "-")
		if ver != "" {
			id += "-" + ver
		}
		return id
	case u == "MIT":
		return "MIT"
	case strings.HasPrefix(u, "APACHE"):
		return "Apache-2.0"
	case strings.HasPrefix(u, "BSD"):
		return strings.ReplaceAll(l, " ", "-")
	}
	return l
}

// drawioSVG: an entry's image as SVG bytes — an SVG data URI decoded; a PNG / JPEG wrapped in an SVG of its size.
func drawioSVG(e drawioEntry) ([]byte, bool, error) {
	d := e.Data
	comma := strings.IndexByte(d, ',')
	if !strings.HasPrefix(d, "data:") || comma < 0 {
		return nil, false, fmt.Errorf("not a data URI")
	}
	head, body := d[5:comma], d[comma+1:]
	mime := strings.SplitN(head, ";", 2)[0]
	switch {
	case mime == "image/svg+xml":
		if strings.Contains(head, ";base64") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
			if err != nil {
				b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(body), "="))
			}
			return b, false, err
		}
		s, err := url.PathUnescape(body)
		if err != nil {
			s = body
		}
		return []byte(s), false, nil
	case mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" || mime == "image/webp":
		w, h := e.W, e.H
		if w <= 0 || h <= 0 {
			w, h = 64, 64
		}
		return []byte(fmt.Sprintf(`<svg xmlns="%s" viewBox="0 0 %g %g" width="%g" height="%g"><image width="%g" height="%g" href="%s"/></svg>`,
			svgNS, w, h, w, h, w, h, d)), true, nil
	}
	return nil, false, fmt.Errorf("unsupported image %q", mime)
}

// meaningfulIDs: ids inside an SVG that read as words ("scaning_identity", "graph-1"), not an editor's generated ones
// ("path123", "Layer_1", "svg2108", "linearGradient4").
var idAttr = regexp.MustCompile(`\bid="([^"]{3,60})"`)
var generatedID = regexp.MustCompile(`(?i)^(svg|path|g|layer|defs|rect|circle|ellipse|polygon|polyline|line|text|tspan|use|image|clip|clippath|mask|linear|lineargradient|radial|radialgradient|stop|pattern|filter|metadata|namedview|surface|group|shape|object|symbol|marker|base|xmlid|_x[0-9a-f]+_|color|swatch|title|desc|a|b)[-_]?\d*[a-z]?\d*$|^[-_\d.]+$|^[a-z]?\d+$`)

func meaningfulIDs(svg []byte) []string {
	var out []string
	for _, m := range idAttr.FindAllSubmatch(svg, 40) {
		id := string(m[1])
		if generatedID.MatchString(id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
