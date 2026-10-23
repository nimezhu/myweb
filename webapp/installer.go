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
	notes    string // the latest version's release notes
	checking bool
}

// state.json in the app's folder.
type state struct {
	Name     string            `json:"name"`
	Manifest string            `json:"manifest"`
	Key      string            `json:"key,omitempty"`     // the public key, so -app can start it again
	Keys     []string          `json:"keys,omitempty"`    // the keys the publisher signs with, from its manifest
	Retired  []string          `json:"retired,omitempty"` // keys it stopped signing with: never trusted again
	Current  string            `json:"current"`
	Pin      string            `json:"pin,omitempty"` // chosen with Use: serve it, don't update
	Versions []string          `json:"versions"`
	Notes    map[string]string `json:"notes,omitempty"` // release notes by version
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
	} else if in.state.Pin != "" {
		in.state.Current = in.state.Pin
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

// Status is a snapshot of the app for pages and tools (e.g. /cmd/app).
type Status struct {
	Name        string            `json:"name"`
	Manifest    string            `json:"manifest"`
	Current     string            `json:"current"`          // "" while nothing is installed
	Latest      string            `json:"latest,omitempty"` // newest version the server offers, once checked
	LatestNotes string            `json:"latestNotes,omitempty"`
	Status      string            `json:"status"` // e.g. "up to date", "downloading 1.2.0…", an error
	Checking    bool              `json:"checking"`
	Checked     *time.Time        `json:"checked,omitempty"` // last check
	Updates     bool              `json:"updates"`           // false when pinned or -no-update
	Pinned      string            `json:"pinned,omitempty"`  // the version chosen with Use or -web-version
	Installed   []string          `json:"installed"`         // newest first
	Notes       map[string]string `json:"notes,omitempty"`   // release notes of installed versions
}

// Status reports the app's state.
func (in *Installer) Status() Status {
	installed := in.Installed()
	in.mu.Lock()
	defer in.mu.Unlock()
	st := Status{
		Name: in.state.Name, Manifest: in.opts.ManifestURL, Current: in.state.Current,
		Latest: in.latest, LatestNotes: in.notes, Status: in.status, Checking: in.checking,
		Updates: in.opts.Pin == "" && !in.opts.NoUpdate && in.state.Pin == "", Installed: installed, Notes: map[string]string{},
		Pinned: in.state.Pin,
	}
	if in.opts.Pin != "" {
		st.Pinned = in.opts.Pin
	}
	if !in.checked.IsZero() {
		t := in.checked
		st.Checked = &t
	}
	for v, n := range in.state.Notes {
		st.Notes[v] = n
	}
	return st
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

func (in *Installer) check(ctx context.Context) error {
	in.mu.Lock()
	if in.checking {
		in.mu.Unlock()
		return nil
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

	in.refresh()
	m, base, err := in.fetchManifest(ctx)
	if err != nil {
		return in.fail("cannot check for updates: %v", err)
	}
	in.mu.Lock()
	in.latest, in.notes = m.Version, m.Notes
	current := in.state.Current
	if in.state.Name != "" && in.state.Name != m.Name {
		in.mu.Unlock()
		return in.fail("the manifest is for %q, not %q", m.Name, in.state.Name)
	}
	changed := false
	switch {
	case len(m.Keys) > 0 && strings.Join(m.Keys, ",") != strings.Join(in.state.Keys, ","):
		// trust exactly the keys the publisher signs with now: this follows a
		// key rotation, and drops the old key once rotation is finished
		for _, k := range append(in.state.Keys, in.state.Key) {
			if k != "" && !contains(m.Keys, k) && !contains(in.state.Retired, k) {
				in.state.Retired = append(in.state.Retired, k)
			}
		}
		var retired []string
		for _, k := range in.state.Retired {
			if !contains(m.Keys, k) {
				retired = append(retired, k)
			}
		}
		in.state.Keys, in.state.Retired = append([]string(nil), m.Keys...), retired
		in.state.Key = m.Keys[0]
		changed = true
	case len(in.state.Keys) == 0 && in.state.Key != EncodePublicKey(in.opts.PublicKey):
		in.state.Key = EncodePublicKey(in.opts.PublicKey) // installed before keys were saved
		changed = true
	}
	if changed && current != "" {
		in.saveStateLocked()
	}
	pin := in.state.Pin
	in.mu.Unlock()
	if pin != "" {
		in.setStatus("pinned to " + pin)
		return nil
	}
	if !satisfies(in.opts.Myweb, m.Requires.Myweb) {
		return in.fail("%s %s needs myweb %s (this is %s)", m.Name, m.Version, m.Requires.Myweb, in.opts.Myweb)
	}
	if current != "" && CompareVersions(m.Version, current) <= 0 {
		// same or older: never move backwards by ourselves
		in.setStatus("up to date")
		return nil
	}
	if err := in.install(ctx, m, base); err != nil {
		return in.fail("cannot install %s %s: %v", m.Name, m.Version, err)
	}
	in.setStatus("up to date")
	log.Printf("app: %s %s installed", m.Name, m.Version)
	if current == "" {
		log.Printf("app: next time, start it with -app %s", m.Name)
	}
	if in.opts.OnUpdate != nil {
		in.opts.OnUpdate(m.Name, m.Version)
	}
	return nil
}

func (in *Installer) fail(format string, a ...interface{}) error {
	msg := fmt.Sprintf(format, a...)
	log.Printf("app: %s", msg)
	in.setStatus(msg)
	return errors.New(msg)
}

// trusted are the keys a manifest may be signed with: the one myweb was given
// (unless the publisher has retired it) and the ones the publisher's last
// verified manifest named.
func (in *Installer) trusted() []ed25519.PublicKey {
	in.mu.Lock()
	defer in.mu.Unlock()
	var keys []ed25519.PublicKey
	if !contains(in.state.Retired, EncodePublicKey(in.opts.PublicKey)) {
		keys = append(keys, in.opts.PublicKey)
	}
	for _, k := range in.state.Keys {
		if pk, err := ParsePublicKey(k); err == nil {
			keys = append(keys, pk)
		}
	}
	return keys
}

// Refresh picks up what another process (e.g. "myweb app use") saved for
// this app, and serves its current version.
func (in *Installer) Refresh() { in.refresh() }

func (in *Installer) refresh() {
	if in.opts.Pin != "" {
		return
	}
	in.mu.Lock()
	before := in.state.Current
	in.loadState()
	cur, name := in.state.Current, in.state.Name
	in.mu.Unlock()
	if cur != "" && cur != before {
		in.use(cur)
		if before != "" && in.opts.OnUpdate != nil {
			in.opts.OnUpdate(name, cur)
		}
	}
}

// Available fetches the signed list of every published version, newest
// first (the publisher's <channel>.versions.json).
func (in *Installer) Available(ctx context.Context) (*Versions, error) {
	u := VersionsURL(in.opts.ManifestURL)
	raw, err := in.get(ctx, u, maxManifest*16)
	if err != nil {
		return nil, fmt.Errorf("%v (published with an older myweb-publish? the next build adds the list)", err)
	}
	sig, err := in.get(ctx, u+".sig", 1024)
	if err != nil {
		return nil, err
	}
	if _, err := Verify(raw, sig, in.trusted()...); err != nil {
		return nil, fmt.Errorf("version list: %v", err)
	}
	var vs Versions
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, fmt.Errorf("version list: %v", err)
	}
	in.mu.Lock()
	name := in.state.Name
	in.mu.Unlock()
	if name != "" && vs.Name != name {
		return nil, fmt.Errorf("the version list is for %q, not %q", vs.Name, name)
	}
	for i := range vs.Versions {
		if err := vs.Versions[i].Validate(); err != nil {
			return nil, err
		}
		if vs.Versions[i].Name != vs.Name {
			return nil, fmt.Errorf("version list: %s is for %q", vs.Versions[i].Version, vs.Versions[i].Name)
		}
	}
	vs.Sort()
	return &vs, nil
}

// Use serves the given version from now on and stops updating, installing it
// first if needed (verified like any update). "latest" follows updates again.
func (in *Installer) Use(ctx context.Context, version string) error {
	if in.opts.Pin != "" {
		return fmt.Errorf("this run serves %s (-web-version); start without it to choose a version", in.opts.Pin)
	}
	in.refresh()
	if version == "latest" {
		in.mu.Lock()
		in.state.Pin = ""
		err := in.saveStateLocked()
		in.mu.Unlock()
		if err != nil {
			return err
		}
		return in.check(ctx)
	}
	in.mu.Lock()
	before := in.state.Current
	in.mu.Unlock()
	if !in.installed(version) {
		vs, err := in.Available(ctx)
		if err != nil {
			return err
		}
		m, ok := vs.Find(version)
		if !ok {
			return fmt.Errorf("version %s is not published", version)
		}
		if !satisfies(in.opts.Myweb, m.Requires.Myweb) {
			return fmt.Errorf("%s %s needs myweb %s (this is %s)", m.Name, m.Version, m.Requires.Myweb, in.opts.Myweb)
		}
		base, _ := url.Parse(VersionsURL(in.opts.ManifestURL))
		in.mu.Lock()
		in.state.Pin = version // so that the install's prune keeps it
		in.mu.Unlock()
		if err := in.install(ctx, m, base); err != nil {
			in.mu.Lock()
			in.state.Pin = ""
			in.mu.Unlock()
			return err
		}
	}
	in.mu.Lock()
	in.state.Current, in.state.Pin = version, version
	err := in.saveStateLocked()
	name := in.state.Name
	in.mu.Unlock()
	if err != nil {
		return err
	}
	in.use(version)
	in.setStatus("pinned to " + version)
	if version != before && in.opts.OnUpdate != nil {
		in.opts.OnUpdate(name, version)
	}
	return nil
}

// Update checks for a new version now and installs it, waiting until done;
// it reports what a background check would only log.
func (in *Installer) Update(ctx context.Context) error {
	return in.check(ctx)
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
	if _, err := Verify(raw, sig, in.trusted()...); err != nil {
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
	if len(in.state.Keys) == 0 {
		in.state.Key = EncodePublicKey(in.opts.PublicKey)
	}
	in.state.Current = m.Version
	if m.Notes != "" {
		if in.state.Notes == nil {
			in.state.Notes = map[string]string{}
		}
		in.state.Notes[m.Version] = m.Notes
	}
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
	keep := map[string]bool{in.state.Current: true, in.state.Pin: true}
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
	for v := range in.state.Notes {
		if !contains(kept, v) {
			delete(in.state.Notes, v)
		}
	}
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
	if !contains(vs, st.Pin) {
		st.Pin = ""
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
