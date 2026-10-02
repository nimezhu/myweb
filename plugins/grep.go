package plugins

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

const (
	grepTimeout     = 10 * time.Second
	grepMaxFile     = 4 << 20 // larger files are skipped
	grepMaxLine     = 1 << 20 // longer lines end the scan of that file
	grepLineContext = 120     // characters shown around a match
)

type grepParams struct {
	Pattern    string `json:"pattern"`
	Dir        string `json:"dir"`
	Glob       string `json:"glob"`       // only files whose name matches, e.g. "*.md"
	Regex      bool   `json:"regex"`      // pattern is a regular expression (default: plain text)
	IgnoreCase *bool  `json:"ignoreCase"` // default true
	Limit      int    `json:"limit"`      // matching lines, default 200, max 2000
}

type grepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type grepResult struct {
	Matches   []grepMatch `json:"matches"`
	Files     int         `json:"files"`     // files searched
	Skipped   int         `json:"skipped"`   // binary or too large
	Truncated bool        `json:"truncated"` // stopped at the limit or the time limit
}

var errGrepDone = errors.New("done")

func cmdGrep(ctx context.Context, req *myserver.CmdRequest) (interface{}, error) {
	p := grepParams{Dir: "/", Limit: 200}
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.Pattern == "" {
		return nil, myserver.Errorf(http.StatusBadRequest, "pattern is required")
	}
	if p.Glob != "" {
		if _, err := path.Match(p.Glob, ""); err != nil {
			return nil, myserver.Errorf(http.StatusBadRequest, "bad glob: %v", err)
		}
	}
	if p.Limit <= 0 || p.Limit > 2000 {
		p.Limit = 2000
	}
	expr := p.Pattern
	if !p.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if p.IgnoreCase == nil || *p.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, myserver.Errorf(http.StatusBadRequest, "bad pattern: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	root := req.Server.ResolvePath("/")
	base := req.Server.ResolvePath(p.Dir)
	if !req.Visible(p.Dir, true) {
		return nil, myserver.Errorf(http.StatusNotFound, "not found: %s", p.Dir)
	}
	res := grepResult{Matches: []grepMatch{}}
	err = filepath.WalkDir(base, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip
		}
		if ctx.Err() != nil {
			res.Truncated = true
			return errGrepDone
		}
		urlPath := urlOf(root, fp)
		if d.IsDir() {
			if fp != base && (strings.HasPrefix(d.Name(), ".") || !req.Visible(urlPath, true)) { // .git, .myweb, not yours
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !req.Can(myserver.ActRead, urlPath) {
			return nil
		}
		if p.Glob != "" {
			if ok, _ := path.Match(p.Glob, d.Name()); !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > grepMaxFile {
			res.Skipped++
			return nil
		}
		searched, err := grepFile(fp, urlPath, re, &res, p.Limit)
		if searched {
			res.Files++
		} else {
			res.Skipped++
		}
		return err
	})
	if err != nil && err != errGrepDone {
		return nil, err
	}
	return res, nil
}

// grepFile appends matching lines; it reports false for binary files.
func grepFile(fp, urlPath string, re *regexp.Regexp, res *grepResult, limit int) (bool, error) {
	f, err := os.Open(fp)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	br := bufio.NewReader(f)
	head, _ := br.Peek(8000)
	if bytes.IndexByte(head, 0) >= 0 {
		return false, nil
	}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64<<10), grepMaxLine)
	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		loc := re.FindIndex(line)
		if loc == nil {
			continue
		}
		if len(res.Matches) >= limit {
			res.Truncated = true
			return true, errGrepDone
		}
		res.Matches = append(res.Matches, grepMatch{urlPath, n, excerpt(line, loc)})
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return true, nil // e.g. a huge line: keep what was found
	}
	return true, nil
}

// excerpt shows the match with some context, trimmed to whole characters.
func excerpt(line []byte, loc []int) string {
	start, end := max(0, loc[0]-grepLineContext/2), min(len(line), loc[1]+grepLineContext/2)
	for start > 0 && start < len(line) && line[start]&0xC0 == 0x80 {
		start--
	}
	for end < len(line) && line[end]&0xC0 == 0x80 {
		end++
	}
	s := strings.TrimSpace(string(line[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(line) {
		s += "…"
	}
	return s
}
