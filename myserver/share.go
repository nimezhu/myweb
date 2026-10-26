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
// not seen, up to its quota (DefaultShareMaxMB and DefaultShareMaxFiles
// unless given), so a link can't fill the disk before it expires. A link reaches only its folder, as an installed app does (see
// appaccess.go): not .myweb, and not where a symbolic link in it leads
// elsewhere. Links are kept in .myweb/shares.json, with their secret so the
// person who made one can copy it again.
//
// A link may have a password: then its pages ask for it once per browser
// (only a salted PBKDF2 hash of it is kept; wrong guesses are slowed down).
// A link to read may also end after so many downloads (files and zips).
//
// Commands (with the token, not from apps):
//
//	POST /cmd/shareCreate  {"path":"/photos","upload":false,"days":7}
//	POST /cmd/shares       the links, without their secrets
//	POST /cmd/shareRevoke  {"id":"…"}

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
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
	ID      string    `json:"id"`             // names it in lists, to revoke it
	Hash    string    `json:"hash,omitempty"` // SHA-256 of its secret, hex
	Path    string    `json:"path"`
	Upload  bool      `json:"upload,omitempty"` // a drop box
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	By      string    `json:"by,omitempty"` // multi-user: who made it

	// an upload link's quota, and what it has taken
	MaxBytes int64 `json:"maxBytes,omitempty"`
	MaxFiles int   `json:"maxFiles,omitempty"`
	Bytes    int64 `json:"bytes,omitempty"`
	Files    int   `json:"files,omitempty"`

	// a link to read: how many files and zips were downloaded, and after how
	// many it ends (0: no limit)
	Downloads    int `json:"downloads,omitempty"`
	MaxDownloads int `json:"maxDownloads,omitempty"`

	Secret   string `json:"secret,omitempty"`   // to show the link again
	PassSalt string `json:"passSalt,omitempty"` // a password: hex salt and PBKDF2-SHA256
	PassHash string `json:"passHash,omitempty"`
}

// usedUp reports whether a link has had its downloads.
func (sh *Share) usedUp() bool { return sh.MaxDownloads > 0 && sh.Downloads >= sh.MaxDownloads }

const sharePassIter = 210_000

func sharePassHash(pw string, salt []byte) string {
	k, _ := pbkdf2.Key(sha256.New, pw, salt, sharePassIter, 32)
	return hex.EncodeToString(k)
}

// The quota of an upload link when none is given.
const (
	DefaultShareMaxMB    = 1024
	DefaultShareMaxFiles = 500
)

// ShareOptions are how a link is made; zero values are the defaults.
type ShareOptions struct {
	Upload   bool
	Days     int   // default 7
	MaxMB    int64 // an upload link's quota
	MaxFiles int
	By       string

	Password     string // "": none
	MaxDownloads int    // a link to read: ends after so many downloads (0: no limit)
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

// CreateShare makes a link to p (a folder for an upload link), and returns
// it with its secret, the part of the link that opens it.
func (s *Server) CreateShare(p string, o ShareOptions) (Share, string, error) {
	upload, days := o.Upload, o.Days
	if days == 0 {
		days = 7
	}
	if o.MaxMB == 0 {
		o.MaxMB = DefaultShareMaxMB
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = DefaultShareMaxFiles
	}
	if o.MaxMB < 0 || o.MaxFiles < 0 || o.MaxDownloads < 0 {
		return Share{}, "", errors.New("a limit can't be negative")
	}
	if o.Password != "" && len([]rune(o.Password)) < 4 {
		return Share{}, "", errors.New("a password needs at least 4 characters")
	}
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
		Created: now, Expires: now.Add(time.Duration(days) * 24 * time.Hour), By: o.By}
	if upload {
		sh.MaxBytes, sh.MaxFiles = o.MaxMB<<20, o.MaxFiles
	} else {
		sh.MaxDownloads = o.MaxDownloads
	}
	sh.Secret = secret
	if o.Password != "" {
		salt := make([]byte, 16)
		rand.Read(salt)
		sh.PassSalt, sh.PassHash = hex.EncodeToString(salt), sharePassHash(o.Password, salt)
	}
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
	if u := s.PublicURL(); u != "" { // a tunnel
		base = u
	} else if lan := s.LANURLs(); len(lan) > 0 {
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
	if sh.usedUp() {
		sharePage(w, http.StatusGone, "Link used up", "<p>This link has had all its downloads.</p>")
		return
	}
	if sh.PassHash != "" && !s.shareUnlocked(w, r, sh, secret) {
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
		if rg := r.Header.Get("Range"); r.Method == http.MethodGet && (rg == "" || strings.HasPrefix(rg, "bytes=0-")) {
			s.countShareDownload(sh.ID) // not each piece of one download
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, path.Base(r.URL.Path)+"/", http.StatusMovedPermanently)
		return
	}
	if _, ok := r.URL.Query()["zip"]; ok {
		if r.Method == http.MethodGet {
			s.countShareDownload(sh.ID)
		}
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
	full := func() {
		sharePage(w, http.StatusRequestEntityTooLarge, title, receivedList(got)+"<p>This link has taken all it can. Ask for a new one to send more.</p>")
	}
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
		now := s.shareByID(sh.ID) // what it has taken so far, with other uploads
		if now == nil {
			part.Close()
			sharePage(w, http.StatusNotFound, "Link not found", "<p>This link has expired or was removed.</p>")
			return
		}
		left := now.MaxBytes - now.Bytes
		if left <= 0 || now.Files >= now.MaxFiles {
			part.Close()
			full()
			return
		}
		max := min(s.maxUpload(), left)
		tmp, size, err := receive(dir, part, max, io.Discard)
		part.Close()
		if errors.Is(err, errTooLarge) {
			if max < s.maxUpload() {
				sharePage(w, http.StatusRequestEntityTooLarge, title, receivedList(got)+fmt.Sprintf("<p>%s is more than this link can still take (%s).</p>", html.EscapeString(name), humanSize(max))+form)
				return
			}
			sharePage(w, http.StatusRequestEntityTooLarge, title, receivedList(got)+fmt.Sprintf("<p>%s is too large (at most %s).</p>", html.EscapeString(name), humanSize(max))+form)
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
		s.countShareUpload(sh.ID, size)
		s.record(Activity{Op: "upload", Path: landed, Size: size, Via: "share"})
		log.Printf("share %s: received %s", sh.ID, landed)
	}
	if len(got) == 0 {
		sharePage(w, http.StatusBadRequest, title, "<p>Choose some files to send.</p>"+form)
		return
	}
	sharePage(w, http.StatusOK, "Thank you", receivedList(got)+`<p><a href="">Send more</a></p>`)
}

// receivedList tells which files arrived ("" for none).
func receivedList(got []string) string {
	if len(got) == 0 {
		return ""
	}
	var list strings.Builder
	for _, n := range got {
		list.WriteString("<li>" + html.EscapeString(n) + "</li>")
	}
	return fmt.Sprintf("<p>Received %d file%s:</p><ul>%s</ul>", len(got), plural(len(got)), list.String())
}

func (s *Server) shareByID(id string) *Share {
	list, err := s.Shares()
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// shareUnlocked reports whether r's browser gave the link's password; if
// not, it answers: the form, or the password just sent checked.
func (s *Server) shareUnlocked(w http.ResponseWriter, r *http.Request, sh *Share, secret string) bool {
	key := shareKey(sh)
	if c, err := r.Cookie(shareCookie); err == nil && hmac.Equal([]byte(c.Value), []byte(key)) {
		return true
	}
	form := `<form method="post" action="?unlock"><p><input type="password" name="password" autofocus required aria-label="Password" placeholder="Password"></p><p><button>Open</button></p></form>`
	if _, ok := r.URL.Query()["unlock"]; !ok || r.Method != http.MethodPost {
		sharePage(w, http.StatusUnauthorized, "Password", "<p>This link needs a password.</p>"+form)
		return false
	}
	if !shareTries.allow(sh.ID) {
		sharePage(w, http.StatusTooManyRequests, "Password", "<p>Too many wrong passwords. Try again in a few minutes.</p>")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	salt, _ := hex.DecodeString(sh.PassSalt)
	if r.ParseForm() != nil || !hmac.Equal([]byte(sharePassHash(r.PostForm.Get("password"), salt)), []byte(sh.PassHash)) {
		shareTries.fail(sh.ID)
		log.Printf("share %s: wrong password", sh.ID)
		sharePage(w, http.StatusUnauthorized, "Password", "<p>That is not the password.</p>"+form)
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: shareCookie, Value: key, Path: "/_s/" + secret + "/",
		HttpOnly: true, Secure: s.cfg.TLS != nil, SameSite: http.SameSiteLaxMode, Expires: sh.Expires})
	http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
	return false
}

const shareCookie = "myweb_share"

// shareKey is what a browser that gave the password keeps: it can't be
// made without the password's hash, which never leaves the server.
func shareKey(sh *Share) string {
	k, _ := hex.DecodeString(sh.PassHash)
	m := hmac.New(sha256.New, k)
	m.Write([]byte("unlock " + sh.ID))
	return hex.EncodeToString(m.Sum(nil))
}

// shareTries limits wrong passwords: 10 per link per 10 minutes.
var shareTries = &tries{max: 10, window: 10 * time.Minute, fails: map[string][]time.Time{}}

type tries struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func (t *tries) recent(id string) []time.Time {
	var keep []time.Time
	for _, at := range t.fails[id] {
		if time.Since(at) < t.window {
			keep = append(keep, at)
		}
	}
	t.fails[id] = keep
	return keep
}

func (t *tries) allow(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.recent(id)) < t.max
}

func (t *tries) fail(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fails[id] = append(t.recent(id), time.Now())
}

// countShareDownload adds a download to its link's count.
func (s *Server) countShareDownload(id string) {
	s.updateShare(id, func(sh *Share) { sh.Downloads++ })
}

// updateShare changes the link id with f, and saves it.
func (s *Server) updateShare(id string, f func(*Share)) {
	sharesMu.Lock()
	defer sharesMu.Unlock()
	list, err := s.loadShares()
	if err != nil {
		log.Printf("shares: %v", err)
		return
	}
	for i := range list {
		if list[i].ID == id {
			f(&list[i])
		}
	}
	if err := s.saveShares(list); err != nil {
		log.Printf("shares: %v", err)
	}
}

// countShareUpload adds a received file to its link's total.
func (s *Server) countShareUpload(id string, size int64) {
	s.updateShare(id, func(sh *Share) {
		sh.Bytes += size
		sh.Files++
	})
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
			Path         string `json:"path"`
			Upload       bool   `json:"upload"`
			Days         int    `json:"days"`
			MaxMB        int64  `json:"maxMB"`
			MaxFiles     int    `json:"maxFiles"`
			Password     string `json:"password"`
			MaxDownloads int    `json:"maxDownloads"`
		}
		if err := req.Bind(&p); err != nil {
			return nil, err
		}
		act := ActRead
		if p.Upload {
			act = ActWrite
		}
		if !req.Can(act, p.Path) {
			return nil, Errorf(http.StatusForbidden, "no %s permission for %s", act, path.Clean("/"+p.Path))
		}
		sh, secret, err := s.CreateShare(p.Path, ShareOptions{Upload: p.Upload, Days: p.Days, MaxMB: p.MaxMB, MaxFiles: p.MaxFiles, By: userName(req.User),
			Password: p.Password, MaxDownloads: p.MaxDownloads})
		if err != nil {
			return nil, Errorf(http.StatusBadRequest, "%v", err)
		}
		return map[string]interface{}{"id": sh.ID, "url": s.ShareURL(secret), "path": sh.Path,
			"upload": sh.Upload, "expires": sh.Expires, "maxBytes": sh.MaxBytes, "maxFiles": sh.MaxFiles,
			"maxDownloads": sh.MaxDownloads, "password": sh.PassHash != "", "local": !s.exposed()}, nil
	}, Describe(`make a link that opens a folder or file without the token: {"path":"/photos","upload":false,"days":7,"password":"","maxDownloads":0}; upload: a drop box for files, taking up to maxMB (default 1024) and maxFiles (500)`))
	s.RegisterCmd("shares", func(ctx context.Context, req *CmdRequest) (interface{}, error) {
		if err := notApp(req); err != nil {
			return nil, err
		}
		list, err := s.Shares()
		if err != nil {
			return nil, err
		}
		type view struct {
			Share
			URL      string `json:"url,omitempty"` // for whoever made it
			Password bool   `json:"password,omitempty"`
		}
		out := []view{}
		for _, sh := range list {
			mine := s.cfg.Access == nil || sh.By == userName(req.User)
			if !mine && !req.Can(ActAdmin, "/") {
				continue
			}
			v := view{Share: sh, Password: sh.PassHash != ""}
			if mine && sh.Secret != "" {
				v.URL = s.ShareURL(sh.Secret)
			}
			v.Hash, v.Secret, v.PassSalt, v.PassHash = "", "", "", ""
			out = append(out, v)
		}
		return out, nil
	}, Describe("the share links; each one's url for whoever made it"))
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
