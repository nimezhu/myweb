package myserver

// Share links: a link to one folder or file of the data folder that works
// without the token, for people who have nothing else, until it expires or
// is revoked.
//
//	GET  /_s/<secret>/[sub/path]   a shared folder (listed) or file
//	GET  /_s/<secret>/?zip         the shared folder as a .zip
//	GET  /_s/<secret>/             an upload link: a form to send files
//	POST /_s/<secret>/             ... the files, into the shared folder
//
// An upload link is a drop box: files can be added (never overwriting one),
// not seen. A link reaches only its folder, as an installed app does (see
// appaccess.go): not .myweb, and not where a symbolic link in it leads
// elsewhere. Links are kept in .myweb/shares.json by the SHA-256 of their
// secret, so the link itself is shown only when it is made.
//
// Commands (with the token, not from apps):
//
//	POST /cmd/shareCreate  {"path":"/photos","upload":false,"days":7}
//	POST /cmd/shares       the links, without their secrets
//	POST /cmd/shareRevoke  {"id":"…"}

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Share is one share link.
type Share struct {
	ID      string    `json:"id"`   // names it in lists, to revoke it
	Hash    string    `json:"hash"` // SHA-256 of its secret, hex
	Path    string    `json:"path"`
	Upload  bool      `json:"upload,omitempty"` // a drop box
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	By      string    `json:"by,omitempty"` // multi-user: who made it
}

// MaxShareDays is the longest a share link may last.
const MaxShareDays = 365

// MaxShareUploadFiles limits the files one upload to a link may send.
var MaxShareUploadFiles = 1000

var sharesMu sync.Mutex

func (s *Server) sharesFile() string { return filepath.Join(s.cfg.Root, metaDir, "shares.json") }

// Shares lists the share links that haven't expired.
func (s *Server) Shares() ([]Share, error) {
	sharesMu.Lock()
	defer sharesMu.Unlock()
	return s.loadShares()
}

func (s *Server) loadShares() ([]Share, error) {
	b, err := os.ReadFile(s.sharesFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all, out []Share
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s: %v", s.sharesFile(), err)
	}
	now := time.Now()
	for _, sh := range all {
		if now.Before(sh.Expires) {
			out = append(out, sh)
		}
	}
	return out, nil
}

func (s *Server) saveShares(list []Share) error {
	if list == nil {
		list = []Share{}
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.sharesFile(), append(b, '\n'))
}

// CreateShare makes a link to p (a folder for an upload link) lasting days,
// and returns it with its secret, the part of the link that opens it.
func (s *Server) CreateShare(p string, upload bool, days int, by string) (Share, string, error) {
	p = path.Clean("/" + p)
	if IsReserved(p) {
		return Share{}, "", errors.New("the server's own folder can't be shared")
	}
	if days < 1 || days > MaxShareDays {
		return Share{}, "", fmt.Errorf("a link lasts 1 to %d days", MaxShareDays)
	}
	fi, err := os.Stat(s.ResolvePath(p))
	if err != nil {
		return Share{}, "", fmt.Errorf("no such file or folder: %s", p)
	}
	if upload && !fi.IsDir() {
		return Share{}, "", errors.New("an upload link needs a folder")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Share{}, "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC().Truncate(time.Second)
	sh := Share{ID: shareHash(secret)[:10], Hash: shareHash(secret), Path: p, Upload: upload,
		Created: now, Expires: now.Add(time.Duration(days) * 24 * time.Hour), By: by}
	sharesMu.Lock()
	defer sharesMu.Unlock()
	list, err := s.loadShares()
	if err != nil {
		return Share{}, "", err
	}
	if err := s.saveShares(append(list, sh)); err != nil {
		return Share{}, "", err
	}
	return sh, secret, nil
}

// RevokeShare ends a link by its ID.
func (s *Server) RevokeShare(id string) error {
	sharesMu.Lock()
	defer sharesMu.Unlock()
	list, err := s.loadShares()
	if err != nil {
		return err
	}
	out := []Share{}
	for _, sh := range list {
		if sh.ID != id {
			out = append(out, sh)
		}
	}
	if len(out) == len(list) {
		return fmt.Errorf("no share link %s", id)
	}
	return s.saveShares(out)
}

// ShareURL is the link with secret: at the first address other machines
// reach (with -global), else this machine's.
func (s *Server) ShareURL(secret string) string {
	base := s.URL()
	if lan := s.LANURLs(); len(lan) > 0 {
		base = strings.TrimSuffix(lan[0], "/")
	}
	return base + "/_s/" + secret + "/"
}

func shareHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func (s *Server) findShare(secret string) *Share {
	if secret == "" {
		return nil
	}
	list, err := s.Shares()
	if err != nil {
		log.Printf("shares: %v", err)
		return nil
	}
	h := shareHash(secret)
	for i := range list {
		if list[i].Hash == h {
			return &list[i]
		}
	}
	return nil
}

// shareUser is what a link may do: its folder only, as an app's folders.
func shareUser(sh *Share) *User {
	return &User{Name: "share " + sh.ID, Role: "share", isApp: true, app: []Folder{{Path: sh.Path, ReadOnly: !sh.Upload}}}
}

// redactShare keeps a link's secret out of request logs.
func redactShare(uri string) string {
	if rest, ok := strings.CutPrefix(uri, "/_s/"); ok {
		_, sub, _ := strings.Cut(rest, "/")
		return "/_s/…/" + sub
	}
	return uri
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer") // the secret is in the address
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Content-Type-Options", "nosniff")
	rest := strings.TrimPrefix(r.URL.Path, "/_s/")
	secret, sub, slash := strings.Cut(rest, "/")
	sh := s.findShare(secret)
	if sh == nil {
		sharePage(w, http.StatusNotFound, "Link not found", "<p>This link has expired or was removed.</p>")
		return
	}
	if !slash {
		http.Redirect(w, r, secret+"/", http.StatusMovedPermanently)
		return
	}
	u := shareUser(sh)
	if sh.Upload {
		s.shareUpload(w, r, sh, u)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "get only")
		return
	}
	p := path.Clean(sh.Path + "/" + sub)
	fi, err := os.Stat(s.ResolvePath(sh.Path))
	if err == nil && !fi.IsDir() { // a shared file, at the link and at its name
		if sub == "" { // at its name, so it downloads as that
			http.Redirect(w, r, url.PathEscape(path.Base(sh.Path)), http.StatusFound)
			return
		}
		if sub != path.Base(sh.Path) {
			http.NotFound(w, r)
			return
		}
		p = sh.Path
	}
	fi, err = os.Stat(s.ResolvePath(p))
	if err != nil || !s.allowed(u, ActRead, p) {
		sharePage(w, http.StatusNotFound, "Not found", "<p>Nothing is shared at this address.</p>")
		return
	}
	if !fi.IsDir() {
		f, err := os.Open(s.ResolvePath(p))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		// a shared page must not run as this server
		h.Set("Content-Security-Policy", "sandbox")
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, path.Base(r.URL.Path)+"/", http.StatusMovedPermanently)
		return
	}
	if _, ok := r.URL.Query()["zip"]; ok {
		s.shareZip(w, r, u, p)
		return
	}
	s.shareList(w, sh, p)
}

func (s *Server) shareList(w http.ResponseWriter, sh *Share, p string) {
	entries, err := os.ReadDir(s.ResolvePath(p))
	if err != nil {
		http.Error(w, "cannot read the folder", http.StatusInternalServerError)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	var b strings.Builder
	if p != sh.Path {
		b.WriteString(`<tr><td><a href="../">↑ up</a></td><td></td><td></td></tr>`)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || IsReserved(path.Join(p, name)) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		href, label, size := url.PathEscape(name), name, humanSize(info.Size())
		if e.IsDir() {
			href, label, size = href+"/", name+"/", ""
		}
		fmt.Fprintf(&b, `<tr><td><a href="%s">%s</a></td><td class="n">%s</td><td class="n">%s</td></tr>`,
			html.EscapeString(href), html.EscapeString(label), size, info.ModTime().Format("2006-01-02 15:04"))
	}
	if b.Len() == 0 {
		b.WriteString(`<tr><td colspan="3" class="empty">This folder is empty.</td></tr>`)
	}
	title := path.Base(p)
	if p == "/" {
		title = filepath.Base(s.cfg.Root)
	}
	sharePage(w, http.StatusOK, title, fmt.Sprintf(`<p class="sub">Shared until %s · <a href="?zip" download>Download all as .zip</a></p>
<table><thead><tr><th>Name</th><th class="n">Size</th><th class="n">Modified</th></tr></thead><tbody>%s</tbody></table>`,
		sh.Expires.Local().Format("2 Jan 2006"), b.String()))
}

func (s *Server) shareZip(w http.ResponseWriter, r *http.Request, u *User, p string) {
	entries, err := os.ReadDir(s.ResolvePath(p))
	if err != nil {
		http.Error(w, "cannot read the folder", http.StatusInternalServerError)
		return
	}
	base := path.Base(p)
	if p == "/" {
		base = filepath.Base(s.cfg.Root)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base+".zip"))
	if r.Method == http.MethodHead {
		return
	}
	zw := zip.NewWriter(w)
	for _, e := range entries {
		if err := s.zipTree(r.Context(), zw, u, path.Join(p, e.Name()), e.Name()); err != nil {
			log.Printf("zip of a share: %v", err)
			return
		}
	}
	zw.Close()
}

func (s *Server) shareUpload(w http.ResponseWriter, r *http.Request, sh *Share, u *User) {
	form := `<form method="post" enctype="multipart/form-data">
<p><input type="file" name="files" multiple required></p>
<p><button>Send</button></p></form>`
	title := "Send files"
	note := fmt.Sprintf(`<p class="sub">Files you send go to %s's folder <b>%s</b>. You can't see what is there. This link works until %s.</p>`,
		html.EscapeString(sh.byName()), html.EscapeString(path.Base(sh.Path)), sh.Expires.Local().Format("2 Jan 2006"))
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		sharePage(w, http.StatusOK, title, note+form)
		return
	case http.MethodPost:
	default:
		writeError(w, http.StatusMethodNotAllowed, "get or post")
		return
	}
	if s.ReadOnly() {
		sharePage(w, http.StatusForbidden, title, "<p>Uploads are paused: the server is read-only for now.</p>")
		return
	}
	if !s.allowed(u, ActWrite, sh.Path+"/x") {
		sharePage(w, http.StatusForbidden, title, "<p>This folder can't take files.</p>")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		sharePage(w, http.StatusBadRequest, title, "<p>Choose some files to send.</p>"+form)
		return
	}
	dir := s.ResolvePath(sh.Path)
	var got []string
	for len(got) < MaxShareUploadFiles {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			sharePage(w, http.StatusBadRequest, title, "<p>The upload was cut short.</p>"+form)
			return
		}
		if part.FormName() != "files" || part.FileName() == "" {
			part.Close()
			continue
		}
		name := sanitizeFilename(part.FileName())
		if strings.HasPrefix(name, ".") {
			name = "_" + name // no hidden files, nor .myweb
		}
		tmp, _, err := receive(dir, part, s.maxUpload(), io.Discard)
		part.Close()
		if errors.Is(err, errTooLarge) {
			sharePage(w, http.StatusRequestEntityTooLarge, title, fmt.Sprintf("<p>%s is too large (at most %s).</p>", html.EscapeString(name), humanSize(s.maxUpload()))+form)
			return
		}
		if err != nil {
			log.Printf("share upload: %v", err)
			sharePage(w, http.StatusInternalServerError, title, "<p>The file could not be stored.</p>")
			return
		}
		landed, _, _, err := s.place(tmp, path.Join(sh.Path, name), name, "rename")
		if err != nil {
			os.Remove(tmp)
			sharePage(w, http.StatusInternalServerError, title, "<p>The file could not be stored.</p>")
			return
		}
		got = append(got, path.Base(landed))
		log.Printf("share %s: received %s", sh.ID, landed)
	}
	if len(got) == 0 {
		sharePage(w, http.StatusBadRequest, title, "<p>Choose some files to send.</p>"+form)
		return
	}
	var list strings.Builder
	for _, n := range got {
		list.WriteString("<li>" + html.EscapeString(n) + "</li>")
	}
	sharePage(w, http.StatusOK, "Thank you", fmt.Sprintf("<p>Received %d file%s:</p><ul>%s</ul><p><a href=\"\">Send more</a></p>",
		len(got), plural(len(got)), list.String()))
}

func (sh *Share) byName() string {
	if sh.By != "" {
		return sh.By
	}
	return "the owner"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func humanSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// sharePage writes a small page of its own: no scripts, nothing loaded from
// elsewhere.
func sharePage(w http.ResponseWriter, status int, title, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>%[1]s</title><style>
:root{color-scheme:light dark;--fg:#1d1d1f;--bg:#fafafa;--sub:#6e6e73;--line:#e3e3e8;--link:#0a66c2}
@media (prefers-color-scheme:dark){:root{--fg:#ececf0;--bg:#16161a;--sub:#9a9aa3;--line:#2c2c33;--link:#6cb4ff}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}
main{max-width:760px;margin:0 auto;padding:28px 16px}h1{font-size:22px;margin:0 0 6px;overflow-wrap:anywhere}
.sub{color:var(--sub)}a{color:var(--link)}table{width:100%%;border-collapse:collapse;margin-top:14px}
th,td{text-align:left;padding:7px 6px;border-bottom:1px solid var(--line);overflow-wrap:anywhere}
th{font-weight:600;color:var(--sub);font-size:13px}.n{text-align:right;white-space:nowrap;color:var(--sub)}
.empty{color:var(--sub);text-align:center;padding:24px}button{font:inherit;padding:7px 16px}
</style></head><body><main><h1>%[1]s</h1>%[2]s</main></body></html>`, html.EscapeString(title), body)
}

func (s *Server) registerShareCmds() {
	notApp := func(req *CmdRequest) error {
		if req.User.IsApp() {
			return Errorf(http.StatusForbidden, "apps can't make or see share links")
		}
		return nil
	}
	s.RegisterCmd("shareCreate", func(ctx context.Context, req *CmdRequest) (interface{}, error) {
		if err := notApp(req); err != nil {
			return nil, err
		}
		var p struct {
			Path   string `json:"path"`
			Upload bool   `json:"upload"`
			Days   int    `json:"days"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if p.Days == 0 {
			p.Days = 7
		}
		act := ActRead
		if p.Upload {
			act = ActWrite
		}
		if !req.Can(act, p.Path) {
			return nil, Errorf(http.StatusForbidden, "no %s permission for %s", act, path.Clean("/"+p.Path))
		}
		sh, secret, err := s.CreateShare(p.Path, p.Upload, p.Days, userName(req.User))
		if err != nil {
			return nil, Errorf(http.StatusBadRequest, "%v", err)
		}
		return map[string]interface{}{"id": sh.ID, "url": s.ShareURL(secret), "path": sh.Path,
			"upload": sh.Upload, "expires": sh.Expires, "local": !s.Global()}, nil
	}, Describe(`make a link that opens a folder or file without the token: {"path":"/photos","upload":false,"days":7}; upload: a drop box for files`))
	s.RegisterCmd("shares", func(ctx context.Context, req *CmdRequest) (interface{}, error) {
		if err := notApp(req); err != nil {
			return nil, err
		}
		list, err := s.Shares()
		if err != nil {
			return nil, err
		}
		out := []Share{}
		for _, sh := range list {
			if s.cfg.Access == nil || sh.By == userName(req.User) || req.Can(ActAdmin, "/") {
				sh.Hash = ""
				out = append(out, sh)
			}
		}
		return out, nil
	}, Describe("the share links (their secrets are only shown when made)"))
	s.RegisterCmd("shareRevoke", func(ctx context.Context, req *CmdRequest) (interface{}, error) {
		if err := notApp(req); err != nil {
			return nil, err
		}
		var p struct {
			ID string `json:"id"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		if s.cfg.Access != nil && !req.Can(ActAdmin, "/") {
			list, _ := s.Shares()
			mine := false
			for _, sh := range list {
				mine = mine || (sh.ID == p.ID && sh.By == userName(req.User))
			}
			if !mine {
				return nil, Errorf(http.StatusForbidden, "not your share link")
			}
		}
		if err := s.RevokeShare(p.ID); err != nil {
			return nil, Errorf(http.StatusNotFound, "%v", err)
		}
		return map[string]string{"revoked": p.ID}, nil
	}, Describe(`end a share link: {"id":"…"}`))
}
