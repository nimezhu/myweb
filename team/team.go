// Package team is myweb-team's access policy: users with roles, private
// folders and shared folders, kept in a team.json file that the running
// server reloads when it changes.
//
// Roles: admin (everything), member (their /users/<name> folder plus shared
// folders as listed), viewer (like member, but never writes). Folder rules
// apply to a folder and everything in it; the most specific rule decides;
// writers may also read; "*" means everyone signed in.
package team

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

// Roles.
const (
	Admin  = "admin"
	Member = "member"
	Viewer = "viewer"
)

// Everyone in a folder rule means every signed-in user.
const Everyone = "*"

// Config is the content of team.json.
type Config struct {
	Users   []UserRec    `json:"users"`
	Folders []FolderRule `json:"folders"`
}

// UserRec is one user. Only a hash of the sign-in token is stored.
type UserRec struct {
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	TokenHash string    `json:"token_sha256"`
	Created   time.Time `json:"created"`
}

// FolderRule shares a folder (and everything in it).
type FolderRule struct {
	Path  string   `json:"path"`
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// HomeOf is a user's private folder.
func HomeOf(name string) string { return "/users/" + name }

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---- the file ----

// Load reads team.json; a missing file is an empty team.
func Load(file string) (*Config, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %v", file, err)
	}
	return &c, c.validate()
}

// Save writes team.json readable only by its owner (it identifies users).
func (c *Config) Save(file string) error {
	if err := c.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// checkUser validates a name and role before anything is changed.
func checkUser(name, role string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid user name %q: use a-z, 0-9, '.', '_', '-' (up to 32)", name)
	}
	if role != Admin && role != Member && role != Viewer {
		return fmt.Errorf("user %q: role must be admin, member or viewer", name)
	}
	return nil
}

func (c *Config) validate() error {
	seen := map[string]bool{}
	for _, u := range c.Users {
		if err := checkUser(u.Name, u.Role); err != nil {
			return err
		}
		if seen[u.Name] {
			return fmt.Errorf("user %q appears twice", u.Name)
		}
		seen[u.Name] = true
	}
	for _, f := range c.Folders {
		if f.Path != path.Clean("/"+f.Path) || myserver.IsReserved(f.Path) {
			return fmt.Errorf("invalid shared folder %q", f.Path)
		}
	}
	return nil
}

func (c *Config) user(name string) *UserRec {
	for i := range c.Users {
		if c.Users[i].Name == name {
			return &c.Users[i]
		}
	}
	return nil
}

// AddUser adds a user and returns their sign-in token (shown only once).
func (c *Config) AddUser(name, role string) (string, error) {
	if c.user(name) != nil {
		return "", fmt.Errorf("user %q already exists", name)
	}
	if err := checkUser(name, role); err != nil {
		return "", err
	}
	t := newToken()
	c.Users = append(c.Users, UserRec{Name: name, Role: role, TokenHash: hashToken(t), Created: time.Now().UTC().Truncate(time.Second)})
	return t, nil
}

// ResetToken gives a user a new sign-in token; the old one stops working.
func (c *Config) ResetToken(name string) (string, error) {
	u := c.user(name)
	if u == nil {
		return "", fmt.Errorf("no user %q", name)
	}
	t := newToken()
	u.TokenHash = hashToken(t)
	return t, nil
}

// SetRole changes a user's role.
func (c *Config) SetRole(name, role string) error {
	u := c.user(name)
	if u == nil {
		return fmt.Errorf("no user %q", name)
	}
	if err := checkUser(name, role); err != nil {
		return err
	}
	u.Role = role
	return nil
}

// RemoveUser deletes a user (their folder and files stay) and their names
// from folder rules.
func (c *Config) RemoveUser(name string) error {
	if c.user(name) == nil {
		return fmt.Errorf("no user %q", name)
	}
	var users []UserRec
	for _, u := range c.Users {
		if u.Name != name {
			users = append(users, u)
		}
	}
	c.Users = users
	for i := range c.Folders {
		c.Folders[i].Read = without(c.Folders[i].Read, name)
		c.Folders[i].Write = without(c.Folders[i].Write, name)
	}
	return nil
}

// Share sets who may read and write a folder (replacing an earlier rule).
func (c *Config) Share(p string, read, write []string) error {
	p = path.Clean("/" + p)
	if myserver.IsReserved(p) {
		return fmt.Errorf("%s is managed by the server and can't be shared", p)
	}
	for _, n := range append(append([]string{}, read...), write...) {
		if n != Everyone && c.user(n) == nil {
			return fmt.Errorf("no user %q", n)
		}
	}
	c.Unshare(p)
	c.Folders = append(c.Folders, FolderRule{Path: p, Read: read, Write: write})
	sort.Slice(c.Folders, func(i, j int) bool { return c.Folders[i].Path < c.Folders[j].Path })
	return nil
}

// Unshare removes the rule for a folder; it reports whether there was one.
func (c *Config) Unshare(p string) bool {
	p = path.Clean("/" + p)
	var kept []FolderRule
	for _, f := range c.Folders {
		if f.Path != p {
			kept = append(kept, f)
		}
	}
	found := len(kept) != len(c.Folders)
	c.Folders = kept
	return found
}

func without(list []string, name string) []string {
	var out []string
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

// ---- the access policy ----

// Access implements myserver.Access from a team.json it keeps reloading.
type Access struct {
	file string
	root string // the served folder: user folders are created in it

	mu      sync.RWMutex
	cfg     *Config
	byHash  map[string]string // token hash -> user name
	modTime time.Time
	checked time.Time
}

// Open loads team.json for a server whose data folder is root.
func Open(file, root string) (*Access, error) {
	a := &Access{file: file, root: root}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Access) load() error {
	c, err := Load(a.file)
	if err != nil {
		return err
	}
	byHash := map[string]string{}
	for _, u := range c.Users {
		byHash[u.TokenHash] = u.Name
		if u.Role != Admin { // everyone's private folder exists, so it can be listed
			os.MkdirAll(filepath.Join(a.root, filepath.FromSlash(HomeOf(u.Name))), 0o755)
		}
	}
	var mt time.Time
	if fi, err := os.Stat(a.file); err == nil {
		mt = fi.ModTime()
	}
	a.mu.Lock()
	a.cfg, a.byHash, a.modTime = c, byHash, mt
	a.mu.Unlock()
	return nil
}

// refresh reloads team.json if it changed (checked at most once a second),
// so adding, removing or changing users takes effect without a restart.
func (a *Access) refresh() {
	a.mu.RLock()
	recent := time.Since(a.checked) < time.Second
	a.mu.RUnlock()
	if recent {
		return
	}
	a.mu.Lock()
	a.checked = time.Now()
	mt := a.modTime
	a.mu.Unlock()
	fi, err := os.Stat(a.file)
	if err != nil || fi.ModTime().Equal(mt) {
		return
	}
	if err := a.load(); err != nil {
		log.Printf("team: keeping the previous users: %v", err) // a half-edited file must not lock everyone out
	} else {
		log.Printf("team: reloaded %s", a.file)
	}
}

// Summary describes the team for the terminal UI.
func (a *Access) Summary() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	n := map[string]int{}
	for _, u := range a.cfg.Users {
		n[u.Role]++
	}
	return fmt.Sprintf("%s (%s, %s, %s) · %s",
		plural(len(a.cfg.Users), "user"), plural(n[Admin], "admin"), plural(n[Member], "member"),
		plural(n[Viewer], "viewer"), plural(len(a.cfg.Folders), "shared folder"))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Identify finds the user of a sign-in token.
func (a *Access) Identify(r *http.Request, token string) *myserver.User {
	if token == "" {
		return nil
	}
	a.refresh()
	a.mu.RLock()
	defer a.mu.RUnlock()
	name, ok := a.byHash[hashToken(token)]
	if !ok {
		return nil
	}
	u := a.cfg.user(name)
	mu := &myserver.User{Name: u.Name, Role: u.Role}
	if u.Role != Admin {
		mu.Home = HomeOf(u.Name)
	}
	return mu
}

func under(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

func listed(list []string, name string) bool {
	for _, n := range list {
		if n == name || n == Everyone {
			return true
		}
	}
	return false
}

// Can decides by the user's current record, so removing a user or changing
// a role applies at once, also to connections already open.
func (a *Access) Can(u *myserver.User, act myserver.Action, p string) bool {
	a.refresh()
	a.mu.RLock()
	defer a.mu.RUnlock()
	rec := a.cfg.user(u.Name)
	switch {
	case rec == nil:
		return false
	case rec.Role == Admin:
		return true
	case act == myserver.ActAdmin:
		return false
	case act == myserver.ActWrite && rec.Role == Viewer:
		return false
	case under(p, HomeOf(rec.Name)):
		return true
	}
	// the most specific shared folder decides
	var best *FolderRule
	for i := range a.cfg.Folders {
		f := &a.cfg.Folders[i]
		if under(p, f.Path) && (best == nil || len(f.Path) > len(best.Path)) {
			best = f
		}
	}
	if best == nil {
		return false
	}
	if act == myserver.ActWrite {
		return listed(best.Write, rec.Name)
	}
	return listed(best.Read, rec.Name) || listed(best.Write, rec.Name)
}

// CanTraverse: a folder may be seen if something inside it is readable.
func (a *Access) CanTraverse(u *myserver.User, dir string) bool {
	if a.Can(u, myserver.ActRead, dir) {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	rec := a.cfg.user(u.Name)
	if rec == nil {
		return false
	}
	if under(HomeOf(rec.Name), dir) {
		return true
	}
	for _, f := range a.cfg.Folders {
		if under(f.Path, dir) && (listed(f.Read, rec.Name) || listed(f.Write, rec.Name)) {
			return true
		}
	}
	return false
}
