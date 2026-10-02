# svgdex

A searchable catalogue of SVG icons, served read-only over HTTP — for apps that browse, search and insert icons
(e.g. the rx-graphic-editor's library plugin). A separate service from `myweb` (`cmd/svgdex`), sharing its module.

```sh
go build -o svgdex ./cmd/svgdex
svgdex index -set fa-solid -name "Font Awesome (solid)" -license CC-BY-4.0 ~/icons/fontawesome/svgs/solid
svgdex index ~/icons/bioicons                     # id / name / licence from its set.json, else the folder's name
svgdex drawio -set bioicons -name Bioicons -url https://bioicons.com drawio-lib/Bioicons-*.xml   # draw.io libraries
svgdex sets
svgdex search mito cell
svgdex serve -p 8434                              # http://127.0.0.1:8434/ — a test page; the API below
```

Every command takes `-d DIR` (default `$SVGDEX_HOME`, else `~/.svgdex`): `DIR/index.db` (SQLite, full-text search)
and the SVGs by content hash in `DIR/svg/ab/abcd….svg` — the same SVG in two sets is stored once.

## Importing

`svgdex index FOLDER…` (re)imports each folder as one set: every `.svg` below it (hidden folders skipped). Its search
words: the file name ("arrow-left.svg" → arrow left), the sub-folders it sits in, its `<title>` / `<desc>`. Re-indexing
a set replaces it in one transaction. An optional `set.json` in the folder:

```json
{ "id": "bioicons", "name": "Bioicons", "license": "CC-BY-4.0", "author": "…", "url": "https://bioicons.com",
  "icons": { "cells/neuron.svg": { "name": "Neuron", "tags": ["nerve"], "license": "CC0-1.0", "author": "…" } } }
```

Re-importing replaces a set; an import of the same id from ANOTHER source is refused (two folders both called "flags")
unless `-replace`. An import with nothing usable leaves no set.

### draw.io libraries

`svgdex drawio FILE…` imports draw.io library files (`<mxlibrary>` — e.g. Bioicons' category files): one set per file,
or with `-set ID` all of them into one set, each file's name becoming its icons' category (a search word and the first
part of their keys: "Bioicons-Cell_culture.xml" → "cell culture"). An entry's title is its name; a title like
`"neuron | CC-BY 4.0 Unported DBCLS"` also gives its licence (as an SPDX id: CC-BY-4.0) and author. SVG entries are
imported as they are; a PNG / JPEG entry is wrapped in an SVG and tagged `raster`; draw.io's own shapes (compressed
`xml` entries) and images by link cannot be imported and are counted. An entry without a title is named
"<category> <n>" (found by its category and any meaningful ids inside it).

### Cleaning

Every SVG is cleaned before it is stored: no scripts, event attributes, `foreignObject`, external links (only `#id`
and `data:image/…`), DOCTYPE / comments / editor metadata; the SVG namespace and a `viewBox` are added when missing.
It is measured too: size, colours, whether it is one colour (recolourable). Its search words include its own
`<title>` / `<desc>` (the document's — not every shape's, which in a diagram would match everything).

## API (GET only; CORS open to any site unless `-cors` lists origins)

| | |
|---|---|
| `/search?q=mito cell&set=a,b&license=MIT&style=mono\|color&limit=60&offset=0&facets=1` | `{ total, items, next, facets }` — every word must match, as a prefix, loosely: a long word's ending is relaxed and a plural finds its singular ("mitochondria" finds mitochondrium, "antibodies" antibody); ranked: exact name, names starting with the words (shortest first), short names, then relevance. `facets`: counts per set / licence / style. Each item: `id, hash, set, key, name, tags, license, author, w, h, colors, mono, bytes, url` |
| `/suggest?q=mi` | completions of the last word |
| `/sets` | `[{ id, name, license, author, url, count }]` |
| `/icon/<id>` | one item |
| `/svg/<hash>.svg` | the SVG — immutable (cached a year, ETag), gzip when asked, `Content-Security-Policy: default-src 'none'` |

An item's `url` is relative to the service, so it works behind a proxy prefix (`/svg/…` → `svgdex serve`).
