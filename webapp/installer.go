package webapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/internal/fsx"
)

const (
	defaultInterval    = 6 * time.Hour
	defaultKeep        = 3
	defaultMaxDownload = 500 << 20
	maxManifest        = 64 << 10
	maxFiles           = 20000
)

// Options configures an Installer.
type Options struct {
	ManifestURL string            // https://…/stable.json (http only for localhost)
	PublicKey   ed25519.PublicKey // verifies the manifest signature
	Dir         string            // where apps are installed; default ~/.myweb/apps
	Pin         string            // serve this installed version and don't update
	NoUpdate    bool              // serve what is installed; never download
	Interval    time.Duration     // between update checks; default 6h
	Keep        int               // versions kept; default 3
	MaxDownload int64             // largest zip, and largest unpacked size; default 500 MiB
	Myweb       string            // the server's version, for manifest requirements

	// OnUpdate is called after a new version becomes current.
	OnUpdate func(name, version string)
	Client   *http.Client
}

// Installer keeps one app installed and up to date, and serves it.
type Installer struct {
	opts  Options
	home  string // Dir/<hash of the manifest URL>
	serve atomic.Pointer[string]
	kick  chan struct{}

	mu       sync.Mutex
	state    state
	status   string
	checked  time.Time
	latest   string
	checking bool
}

// state.json in the app's folder.
type state struct {
	Name     string   `json:"name"`
	Manifest string   `json:"manifest"`
	Current  string   `json:"current"`
	Versions []string `json:"versions"`
}

// New prepares an installer and, if a version is installed, serves it right
// away. It fails for an insecure URL or a pinned version that isn't there.
func New(opts Options) (*Installer, error) {
	u, err := url.Parse(opts.ManifestURL)
	if err != nil || !secureURL(u) {
		return nil, fmt.Errorf("-web-from must be an https:// URL (http:// only for localhost): %q", opts.ManifestURL)
	}
	if len(opts.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("a public key (-web-key) is required to verify the app")
	}
	if opts.Dir == "" {
		d, err := home.For("myweb")
		if err != nil {
			return nil, err
		}
		opts.Dir = d.Apps()
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.Keep <= 0 {
		opts.Keep = defaultKeep
	}
	if opts.MaxDownload <= 0 {
		opts.MaxDownload = defaultMaxDownload
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	sum := sha256.Sum256([]byte(opts.ManifestURL))
	in := &Installer{opts: opts, home: filepath.Join(opts.Dir, hex.EncodeToString(sum[:6])), kick: make(chan struct{}, 1)}
	in.loadState() // the folder is created on the first install
	in.cleanLeftovers()
	if opts.Pin != "" {
		if !in.installed(opts.Pin) {
			return nil, fmt.Errorf("version %s is not installed (installed: %s)", opts.Pin, strings.Join(in.state.Versions, ", "))
		}
		in.state.Current = opts.Pin
	}
	if in.state.Current != "" {
		in.use(in.state.Current)
		in.status = "installed"
	} else {
		in.status = "not installed yet"
	}
	return in, nil
}

func secureURL(u *url.URL) bool {
	if u.Scheme == "https" && u.Host != "" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Start checks for updates now and then every Interval, until ctx ends.
// Nothing is downloaded when NoUpdate or Pin is set.
func (in *Installer) Start(ctx context.Context) {
	switch {
	case in.opts.Pin != "":
		in.setStatus("pinned")
		return
	case in.opts.NoUpdate:
		in.setStatus("updates off")
		return
	}
	go func() {
		t := time.NewTicker(in.opts.Interval)
		defer t.Stop()
		for {
			in.check(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-in.kick:
			}
		}
	}()
}

// CheckNow asks the running updater to check right away.
func (in *Installer) CheckNow() {
	select {
	case in.kick <- struct{}{}:
	default:
	}
}

// Current is the version being served ("" while nothing is installed).
func (in *Installer) Current() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state.Current
}

// StatusLine summarizes the app for the terminal UI.
func (in *Installer) StatusLine() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	name := in.state.Name
	if name == "" {
		name = "app"
	}
	s := name
	if in.state.Current != "" {
		s += " " + in.state.Current
	}
	s += " · " + in.status
	if !in.checked.IsZero() {
		s += " (checked " + in.checked.Format("15:04") + ")"
	}
	return s
}

func (in *Installer) setStatus(s string) {
	in.mu.Lock()
	in.status = s
	in.mu.Unlock()
}

// ---- serving ----

// FS serves the current version, or a "being installed" page until there is one.
func (in *Installer) FS() http.FileSystem { return appFS{in} }

type appFS struct{ in *Installer }

func (f appFS) Open(name string) (http.File, error) {
	if dir := f.in.serve.Load(); dir != nil {
		return http.Dir(*dir).Open(name)
	}
	// nothing installed yet: "/" is a folder whose index.html is the placeholder
	switch name {
	case "/":
		return &memFile{Reader: bytes.NewReader(nil), dir: true}, nil
	case "/index.html":
		return f.in.placeholder()
	}
	return nil, os.ErrNotExist
}

// use points serving at an installed version's files. A zip with a single
// top-level folder (e.g. "dist/") is served from inside that folder.
func (in *Installer) use(version string) {
	dir := filepath.Join(in.home, version)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		if entries, _ := os.ReadDir(dir); len(entries) == 1 && entries[0].IsDir() {
			dir = filepath.Join(dir, entries[0].Name())
		}
	}
	in.serve.Store(&dir)
}

var placeholderTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta http-equiv="refresh" content="2">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Installing…</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:560px;margin:15vh auto;padding:0 16px;color:#333}
@media(prefers-color-scheme:dark){body{background:#121216;color:#ddd}a{color:#a08cff}}</style></head>
<body><h2>Installing the app…</h2><p>{{.}}</p>
<p>This page reloads by itself. Your files are available in the <a href="/_myweb/">file manager</a> meanwhile.</p></body></html>`))

func (in *Installer) placeholder() (http.File, error) {
	var buf bytes.Buffer
	in.mu.Lock()
	status := in.status
	in.mu.Unlock()
	placeholderTmpl.Execute(&buf, status)
	return &memFile{Reader: bytes.NewReader(buf.Bytes()), size: int64(buf.Len())}, nil
}

// memFile is a tiny in-memory http.File (or, with dir, an empty folder).
type memFile struct {
	*bytes.Reader
	size int64
	dir  bool
}

func (f *memFile) Close() error                       { return nil }
func (f *memFile) Readdir(int) ([]os.FileInfo, error) { return nil, nil }
func (f *memFile) Stat() (os.FileInfo, error)         { return memInfo{f.size, f.dir}, nil }

type memInfo struct {
	n   int64
	dir bool
}

func (i memInfo) Name() string {
	if i.dir {
		return "/"
	}
	return "index.html"
}
func (i memInfo) Size() int64 { return i.n }
func (i memInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0o555
	}
	return 0o444
}
func (memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool      { return i.dir }
func (memInfo) Sys() interface{}   { return nil }

// ---- checking and installing ----

func (in *Installer) check(ctx context.Context) {
	in.mu.Lock()
	if in.checking {
		in.mu.Unlock()
		return
	}
	in.checking = true
	if in.state.Current == "" {
		in.status = "checking…"
	}
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.checking = false
		in.checked = time.Now()
		in.mu.Unlock()
	}()

	m, base, err := in.fetchManifest(ctx)
	if err != nil {
		in.fail("cannot check for updates: %v", err)
		return
	}
	in.mu.Lock()
	in.latest = m.Version
	current := in.state.Current
	if in.state.Name != "" && in.state.Name != m.Name {
		in.mu.Unlock()
		in.fail("the manifest is for %q, not %q", m.Name, in.state.Name)
		return
	}
	in.mu.Unlock()
	if !satisfies(in.opts.Myweb, m.Requires.Myweb) {
		in.fail("%s %s needs myweb %s (this is %s)", m.Name, m.Version, m.Requires.Myweb, in.opts.Myweb)
		return
	}
	if current != "" && CompareVersions(m.Version, current) <= 0 {
		// same or older: never move backwards by ourselves
		in.setStatus("up to date")
		return
	}
	if err := in.install(ctx, m, base); err != nil {
		in.fail("cannot install %s %s: %v", m.Name, m.Version, err)
		return
	}
	in.setStatus("up to date")
	log.Printf("app: %s %s installed", m.Name, m.Version)
	if in.opts.OnUpdate != nil {
		in.opts.OnUpdate(m.Name, m.Version)
	}
}

func (in *Installer) fail(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	log.Printf("app: %s", msg)
	in.setStatus(msg)
}

func (in *Installer) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := in.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: larger than %d bytes", u, limit)
	}
	return b, err
}

func (in *Installer) fetchManifest(ctx context.Context) (*Manifest, *url.URL, error) {
	raw, err := in.get(ctx, in.opts.ManifestURL, maxManifest)
	if err != nil {
		return nil, nil, err
	}
	sig, err := in.get(ctx, in.opts.ManifestURL+".sig", 1024)
	if err != nil {
		return nil, nil, err
	}
	if err := Verify(in.opts.PublicKey, raw, sig); err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("manifest: %v", err)
	}
	if err := m.Validate(); err != nil {
		return nil, nil, err
	}
	base, _ := url.Parse(in.opts.ManifestURL)
	return &m, base, nil
}

func (in *Installer) install(ctx context.Context, m *Manifest, base *url.URL) error {
	zipURL, err := base.Parse(m.URL)
	if err != nil || !secureURL(zipURL) {
		return fmt.Errorf("insecure bundle URL %q", m.URL)
	}
	limit := in.opts.MaxDownload
	if m.Size > limit {
		return fmt.Errorf("bundle is %d bytes, over the %d limit", m.Size, limit)
	}
	if !in.installed(m.Version) {
		in.setStatus(fmt.Sprintf("downloading %s…", m.Version))
		data, err := in.get(ctx, zipURL.String(), limit)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != m.SHA256 {
			return errors.New("bundle does not match the manifest's sha256")
		}
		in.setStatus(fmt.Sprintf("installing %s…", m.Version))
		if err := in.unpack(data, m.Version, limit); err != nil {
			return err
		}
	}
	// switch: the new version serves from the next request on
	in.mu.Lock()
	in.state.Name = m.Name
	in.state.Manifest = in.opts.ManifestURL
	in.state.Current = m.Version
	if !contains(in.state.Versions, m.Version) {
		in.state.Versions = append(in.state.Versions, m.Version)
	}
	err = in.saveStateLocked()
	in.mu.Unlock()
	if err != nil {
		return err
	}
	in.use(m.Version)
	in.prune()
	return nil
}

// unpack extracts a zip into <home>/<version>, via a temp folder renamed at
// the end, refusing paths that climb out, links, and oversized content.
func (in *Installer) unpack(data []byte, version string, limit int64) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("bundle is not a zip: %v", err)
	}
	if len(zr.File) > maxFiles {
		return fmt.Errorf("bundle has more than %d files", maxFiles)
	}
	if err := os.MkdirAll(in.home, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(in.home, ".unpack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var total int64
	for _, f := range zr.File {
		raw := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(raw, "/") || contains(strings.Split(raw, "/"), "..") {
			return fmt.Errorf("bundle path %q leaves the app folder", f.Name)
		}
		name := path.Clean("/" + raw)
		if name == "/" {
			continue
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("bundle entry %q is not a regular file", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(out, io.LimitReader(rc, limit-total+1))
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
		if total += n; total > limit {
			return fmt.Errorf("bundle unpacks to more than %d bytes", limit)
		}
	}
	return fsx.Rename(tmp, filepath.Join(in.home, version))
}

// prune keeps the newest Keep versions, and always the current one.
func (in *Installer) prune() {
	in.mu.Lock()
	defer in.mu.Unlock()
	vs := append([]string(nil), in.state.Versions...)
	sort.Slice(vs, func(i, j int) bool { return CompareVersions(vs[i], vs[j]) > 0 })
	keep := map[string]bool{in.state.Current: true}
	for i := 0; i < len(vs) && i < in.opts.Keep; i++ {
		keep[vs[i]] = true
	}
	var kept []string
	for _, v := range in.state.Versions {
		if keep[v] {
			kept = append(kept, v)
			continue
		}
		// Move it aside first: on Windows a file still being served can't be
		// deleted, and a half-deleted version must never be left installed.
		// If the move fails, keep the version and try again next time.
		aside := filepath.Join(in.home, ".old-"+v)
		os.RemoveAll(aside)
		if err := renameDir(filepath.Join(in.home, v), aside); err != nil {
			kept = append(kept, v)
			continue
		}
		os.RemoveAll(aside)
	}
	in.state.Versions = kept
	in.saveStateLocked()
	in.cleanLeftovers()
}

// renameDir is replaced in tests to simulate a version in use.
var renameDir = fsx.Rename

// cleanLeftovers removes what an interrupted unpack or prune left behind.
func (in *Installer) cleanLeftovers() {
	entries, _ := os.ReadDir(in.home)
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, ".old-") || strings.HasPrefix(n, ".unpack-") {
			os.RemoveAll(filepath.Join(in.home, n))
		}
	}
}

func (in *Installer) installed(version string) bool {
	if !versionRe.MatchString(version) {
		return false
	}
	fi, err := os.Stat(filepath.Join(in.home, version))
	return err == nil && fi.IsDir()
}

func (in *Installer) loadState() {
	b, err := os.ReadFile(filepath.Join(in.home, "state.json"))
	if err != nil {
		return
	}
	var st state
	if json.Unmarshal(b, &st) != nil {
		return
	}
	// trust only versions that are really there
	var vs []string
	for _, v := range st.Versions {
		if in.installed(v) {
			vs = append(vs, v)
		}
	}
	st.Versions = vs
	if !contains(vs, st.Current) {
		st.Current = ""
	}
	in.state = st
}

func (in *Installer) saveStateLocked() error {
	b, _ := json.MarshalIndent(in.state, "", "  ")
	tmp := filepath.Join(in.home, ".state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return fsx.Rename(tmp, filepath.Join(in.home, "state.json"))
}

// Installed lists the installed versions, newest first.
func (in *Installer) Installed() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	vs := append([]string(nil), in.state.Versions...)
	sort.Slice(vs, func(i, j int) bool { return CompareVersions(vs[i], vs[j]) > 0 })
	return vs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
