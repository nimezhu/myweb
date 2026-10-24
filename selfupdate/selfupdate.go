// Package selfupdate replaces the running binary with one from a GitHub
// release. A release (built by .github/workflows/release.yml from a v* tag)
// holds one binary per platform, named PROGRAM_VERSION_OS_ARCH[.exe], and a
// checksums.txt with their SHA-256 sums.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// API is GitHub's REST API; replaced in tests.
var API = "https://api.github.com"

// Repo is where releases are published.
const Repo = "nimezhu/myweb"

// Release is one published version.
type Release struct {
	Version string // without the v
	Tag     string
	Notes   string
	URL     string // the release page
	Assets  map[string]string
}

// Updater finds and installs releases of one program.
type Updater struct {
	Program string // e.g. myweb
	Current string // the running version
	Client  *http.Client
	GOOS    string // default: runtime.GOOS
	GOARCH  string
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Latest is the newest release (GitHub's "latest": not a draft or prerelease).
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	return u.release(ctx, "/repos/"+Repo+"/releases/latest")
}

// Get is the release of one version, e.g. 0.0.2 or v0.0.2.
func (u *Updater) Get(ctx context.Context, version string) (*Release, error) {
	return u.release(ctx, "/repos/"+Repo+"/releases/tags/v"+strings.TrimPrefix(version, "v"))
}

func (u *Updater) release(ctx context.Context, path string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", API+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", u.Program+"/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("no such release on github.com/" + Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("github: %s", resp.Status)
	}
	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("github: %v", err)
	}
	rel := &Release{Version: strings.TrimPrefix(r.Tag, "v"), Tag: r.Tag, Notes: r.Body, URL: r.URL, Assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// AssetName is the binary for this platform in a release.
func (u *Updater) AssetName(version string) string {
	goos, goarch := u.GOOS, u.GOARCH
	if goos == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	name := fmt.Sprintf("%s_%s_%s_%s", u.Program, version, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Install downloads the release's binary, checks its checksum and that it
// runs, and puts it in place of exe. The binary it replaces is kept as
// exe.old, for Rollback.
func (u *Updater) Install(ctx context.Context, rel *Release, exe string) error {
	name := u.AssetName(rel.Version)
	url, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("release %s has no binary for this platform (%s)", rel.Tag, name)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}
	sums, err := u.get(ctx, sumsURL, 1<<20)
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	want, err := findSum(sums, name)
	if err != nil {
		return err
	}
	if err := writable(filepath.Dir(exe)); err != nil {
		return err
	}
	bin, err := u.get(ctx, url, 512<<20)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if got := sha256.Sum256(bin); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match its checksum: download it again", name)
	}

	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	defer os.Remove(tmp) // left only on failure
	if err := u.check(tmp, rel.Version); err != nil {
		return err
	}
	return swap(exe, tmp)
}

// check runs the new binary's version command: it must start on this
// machine and be the version it claims.
func (u *Updater) check(bin, version string) error {
	if u.GOOS != "" && (u.GOOS != runtime.GOOS || u.GOARCH != runtime.GOARCH) {
		return nil // tests: a binary for another platform can't run here
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("the new binary does not run: %v", err)
	}
	if f := strings.Fields(string(out)); len(f) < 2 || f[len(f)-1] != version {
		return fmt.Errorf("the new binary says %q, expected version %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// swap moves exe to exe.old and next to exe. Renaming works on a running
// binary, even on Windows; removing it doesn't there, so an exe.old that a
// server not restarted since the last upgrade still runs is moved aside, to
// remove at a later upgrade.
func swap(exe, next string) error {
	old := exe + ".old"
	stale, _ := filepath.Glob(old + ".*.del")
	for _, f := range stale {
		os.Remove(f) // may still run
	}
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		if err := os.Rename(old, fmt.Sprintf("%s.%d.del", old, time.Now().UnixNano())); err != nil {
			return fmt.Errorf("could not move the previous binary aside: %w", err)
		}
	}
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(next, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	return nil
}

// Rollback puts exe.old back in place of exe; exe becomes exe.old, so a
// second rollback undoes the first.
func Rollback(exe string) error {
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return errors.New("no previous version to go back to (" + old + ")")
	}
	if err := writable(filepath.Dir(exe)); err != nil {
		return err
	}
	tmp := exe + ".swap"
	if err := os.Rename(old, tmp); err != nil {
		return err
	}
	if err := os.Rename(exe, old); err != nil {
		os.Rename(tmp, old)
		return err
	}
	return os.Rename(tmp, exe)
}

func (u *Updater) get(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", u.Program+"/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("too large")
	}
	return b, err
}

// findSum reads sha256sum's format: "HEX  NAME" (or "HEX *NAME") per line.
func findSum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no sum for %s", name)
}

// writable tells how to fix a folder the binary can't be replaced in.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".upgrade-*")
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("can't replace the binary in %s (%v): run the upgrade as a user who can write there, or reinstall with go install", dir, err)
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// Newer reports whether version a is newer than b (semantic versions without
// the v; a prerelease is older than its release).
func Newer(a, b string) bool { return compare(a, b) > 0 }

func compare(a, b string) int {
	a, pa, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	b, pb, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	case pa > pb:
		return 1
	}
	return -1
}

// Cached is the latest version, asked of GitHub at most once a day; the
// answer is kept in file. It returns "" when GitHub can't be reached.
func (u *Updater) Cached(ctx context.Context, file string, maxAge time.Duration) string {
	var c struct {
		Checked time.Time `json:"checked"`
		Latest  string    `json:"latest"`
	}
	if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &c) == nil && time.Since(c.Checked) < maxAge {
		return c.Latest
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		return ""
	}
	c.Checked, c.Latest = time.Now().UTC(), rel.Version
	if b, err := json.Marshal(c); err == nil && os.MkdirAll(filepath.Dir(file), 0o755) == nil {
		os.WriteFile(file, b, 0o644)
	}
	return c.Latest
}
