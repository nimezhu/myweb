package svgdex

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Query is a search: words matched as prefixes against an icon's name, tags and set (all of them must match),
// narrowed by sets, licence and style. An empty Q browses (by set, then name).
type Query struct {
	Q       string   `json:"q"`
	Sets    []string `json:"sets,omitempty"`
	License string   `json:"license,omitempty"` // exact, case-insensitive (the icon's own or its set's)
	Style   string   `json:"style,omitempty"`   // "mono" (one colour — recolourable) · "color" · "" any
	Limit   int      `json:"limit,omitempty"`   // default 60, at most 500
	Offset  int      `json:"offset,omitempty"`
	Facets  bool     `json:"facets,omitempty"` // also count the matches per set / licence / style
}

// Result is one page of matches. Next is the offset of the following page (0: none). Facets count every match per
// set, licence and style — each counted with the other filters applied but not its own, so a chip shows how many
// choosing it would give.
type Result struct {
	Total  int                       `json:"total"`
	Items  []Icon                    `json:"items"`
	Next   int                       `json:"next,omitempty"`
	Facets map[string]map[string]int `json:"facets,omitempty"`
}

// Words splits text into lower-case search words: on anything not a letter or digit, and between camelCase parts
// ("ArrowLeft2" → arrow left 2).
func Words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(cur) > 0 && i > 0 {
				prev := rs[i-1]
				lowerToUpper := unicode.IsLower(prev) && unicode.IsUpper(r)
				letterDigit := unicode.IsLetter(prev) != unicode.IsLetter(r)
				if lowerToUpper || letterDigit {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// searchText is what the full-text index holds for a field: its words, and the field as typed (so "camelCase"
// is found as one word too).
func searchText(s string) string {
	w := Words(s)
	whole := strings.ToLower(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " "))
	if j := strings.Join(w, " "); j != whole && whole != "" {
		return j + " " + whole
	}
	return strings.Join(w, " ")
}

var numSuffix = regexp.MustCompile(`[\s_-]+$`)

// NameFromKey is a readable name from an icon's path: its file name without the extension, separators as spaces
// ("cells/nerve_cell-02.svg" → "nerve cell 02").
func NameFromKey(key string) string {
	base := strings.TrimSuffix(path.Base(strings.ReplaceAll(key, "\\", "/")), path.Ext(key))
	base = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(base)
	return strings.Join(strings.Fields(numSuffix.ReplaceAllString(base, "")), " ")
}

// mergeTags joins tag lists: lower-case, no duplicates, in order.
func mergeTags(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, t := range l {
			for _, w := range Words(t) {
				if len(w) < 2 || seen[w] {
					continue
				}
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

// ftsQuery turns typed words into an FTS5 match: every word, as a prefix ("mito cell" → "mito"* "cell"*). A long
// word loosens its ending a little — 7+ letters lose two, 6 lose one — so a word finds its other forms too
// ("mitochondria" → mitochondri*: mitochondrium, mitochondrial; "antibodies" → antibodi*… and "antibody" → antibo*).
func ftsQuery(q string) string {
	var parts []string
	for _, w := range Words(q) {
		// a plural also finds its singular: "antibodies" → antibody, "cells" → cell
		forms := []string{stem(w)}
		switch {
		case strings.HasSuffix(w, "ies") && len(w) > 4:
			forms = append(forms, w[:len(w)-3]+"y")
		case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
			forms = append(forms, stem(w[:len(w)-1]))
		}
		alts := make([]string, 0, len(forms))
		for _, f := range forms {
			alts = append(alts, `"`+f+`"*`)
		}
		if len(alts) == 1 {
			parts = append(parts, alts[0])
		} else {
			parts = append(parts, "("+strings.Join(alts, " OR ")+")")
		}
	}
	return strings.Join(parts, " AND ") // explicit: FTS5 does not take an implicit AND after a ( … ) group
}

func stem(w string) string {
	r := []rune(w)
	switch {
	case len(r) >= 7 && unicode.IsLetter(r[len(r)-1]):
		return string(r[:len(r)-2])
	case len(r) == 6 && unicode.IsLetter(r[len(r)-1]):
		return string(r[:len(r)-1])
	}
	return w
}

// where builds the filter clause, skipping one dimension (for its facet counts).
func (q Query) where(skip string) (string, []any) {
	var conds []string
	var args []any
	if fq := ftsQuery(q.Q); fq != "" {
		conds = append(conds, `i.id IN (SELECT rowid FROM icons_fts WHERE icons_fts MATCH ?)`)
		args = append(args, fq)
	}
	if skip != "set" && len(q.Sets) > 0 {
		conds = append(conds, `i.set_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(q.Sets)), ",")+`)`)
		for _, s := range q.Sets {
			args = append(args, s)
		}
	}
	if skip != "license" && q.License != "" {
		conds = append(conds, `lower(CASE WHEN i.license <> '' THEN i.license ELSE s.license END) = lower(?)`)
		args = append(args, q.License)
	}
	if skip != "style" {
		switch q.Style {
		case "mono":
			conds = append(conds, `i.mono = 1`)
		case "color":
			conds = append(conds, `i.mono = 0`)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Search runs a query.
func (s *Store) Search(q Query) (*Result, error) {
	if q.Limit <= 0 {
		q.Limit = 60
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	const from = ` FROM icons i JOIN sets s ON s.id = i.set_id`
	where, args := q.where("")
	res := &Result{}
	if err := s.db.QueryRow(`SELECT COUNT(*)`+from+where, args...).Scan(&res.Total); err != nil {
		return nil, err
	}
	// Ranking: an exact name first, then names starting with what was typed, then short names (a few words more than
	// typed), then the full-text rank (name counts most, then tags, then the set's name); browsing (no words) goes by
	// set and name.
	order := ` ORDER BY s.name COLLATE NOCASE, i.name COLLATE NOCASE`
	pageArgs := append([]any{}, args...)
	sel := selectIcons
	if fq := ftsQuery(q.Q); fq != "" {
		sel = strings.Replace(selectIcons, "FROM icons i JOIN sets s ON s.id = i.set_id",
			"FROM icons i JOIN sets s ON s.id = i.set_id JOIN (SELECT rowid AS rid, bm25(icons_fts, 10.0, 4.0, 1.0) AS rank FROM icons_fts WHERE icons_fts MATCH ?) f ON f.rid = i.id", 1)
		pageArgs = append([]any{fq}, pageArgs...)
		// exact name · name starting with the words · full-text rank, then shorter names
		order = ` ORDER BY (lower(i.name) = lower(?)) DESC, (lower(i.name) LIKE ? ESCAPE '\') DESC, CASE WHEN lower(i.name) LIKE ? ESCAPE '\' THEN length(i.name) ELSE 0 END, length(i.name) <= length(?) + 12 DESC, f.rank, length(i.name), i.name`
		typed := strings.Join(Words(q.Q), " ")
		like := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(typed) + "%"
		pageArgs = append(pageArgs, typed, like, like, typed)
	}
	pageArgs = append(pageArgs, q.Limit, q.Offset)
	rows, err := s.db.Query(sel+where+order+` LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, err
	}
	res.Items, err = scanIcons(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if q.Offset+len(res.Items) < res.Total {
		res.Next = q.Offset + len(res.Items)
	}
	if q.Facets {
		res.Facets = map[string]map[string]int{}
		for dim, expr := range map[string]string{
			"set":     `i.set_id`,
			"license": `CASE WHEN i.license <> '' THEN i.license ELSE s.license END`,
			"style":   `CASE WHEN i.mono = 1 THEN 'mono' ELSE 'color' END`,
		} {
			w, a := q.where(dim)
			rows, err := s.db.Query(`SELECT `+expr+`, COUNT(*)`+from+w+` GROUP BY 1`, a...)
			if err != nil {
				return nil, err
			}
			m := map[string]int{}
			for rows.Next() {
				var k string
				var n int
				if rows.Scan(&k, &n) == nil {
					m[k] = n
				}
			}
			rows.Close()
			res.Facets[dim] = m
		}
	}
	return res, nil
}

// Suggest completes the last typed word from icon names and tags: up to `limit` words, most used first.
func (s *Store) Suggest(prefix string, limit int) ([]string, error) {
	ws := Words(prefix)
	if len(ws) == 0 {
		return []string{}, nil
	}
	last := ws[len(ws)-1]
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.Query(`SELECT name, tags FROM icons WHERE id IN (SELECT rowid FROM icons_fts WHERE icons_fts MATCH ?) LIMIT 2000`, `"`+last+`"*`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := map[string]int{}
	for rows.Next() {
		var name, tags string
		rows.Scan(&name, &tags)
		for _, w := range append(Words(name), strings.Fields(tags)...) {
			if strings.HasPrefix(w, last) {
				count[w]++
			}
		}
	}
	out := make([]string, 0, len(count))
	for w := range count {
		out = append(out, w)
	}
	sort.Slice(out, func(a, b int) bool {
		if count[out[a]] != count[out[b]] {
			return count[out[a]] > count[out[b]]
		}
		return out[a] < out[b]
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, rows.Err()
}
