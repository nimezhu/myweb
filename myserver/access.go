package myserver

// Multi-user access. A host program (cmd/myweb-team) sets Config.Access to
// decide who each request is and what they may do; every route asks it. With
// no Access, myweb is single-user and the optional Token decides everything.

import (
	"context"
	"net/http"
	"path"
	"strings"
)

// Action is what a request wants to do with a path.
type Action int

const (
	ActRead  Action = iota // read files, list folders, watch, search
	ActWrite               // write, upload, move, delete, restore
	ActAdmin               // server-wide operations (empty the trash, stop the server)
)

func (a Action) String() string {
	return [...]string{"read", "write", "admin"}[a]
}

// User is a signed-in person.
type User struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Home string `json:"home,omitempty"` // their private folder, e.g. /users/alice

	isApp bool     // the limited web app at "/" (see appaccess.go), not a person
	app   []Folder // the folders it may use
}

// IsApp reports whether u is the limited web app rather than a person.
func (u *User) IsApp() bool { return u != nil && u.isApp }

// Access identifies users and decides permissions. Paths are cleaned URL
// paths inside the served folder ("/" is its top).
type Access interface {
	// Identify returns the request's user, or nil if not signed in. token is
	// the Bearer token or sign-in cookie value, "" if there is none.
	Identify(r *http.Request, token string) *User
	// Can reports whether u may do act on p.
	Can(u *User, act Action, p string) bool
	// CanTraverse reports whether u may see that folder dir exists because
	// something inside it is readable (so listings and searches reach it).
	CanTraverse(u *User, dir string) bool
}

type userKey struct{}

// UserFrom returns the signed-in user of a request's context (nil when
// single-user or not signed in).
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(userKey{}).(*User)
	return u
}

// requestToken is the Bearer token or the sign-in cookie value.
func (s *Server) requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(s.cookieName()); err == nil {
		return c.Value
	}
	return ""
}

// identify attaches the user to the request: multi-user, who signed in;
// with a limited app, the app for requests from its pages.
func (s *Server) identify(r *http.Request) *http.Request {
	if s.cfg.Access == nil {
		if s.fromApp(r) {
			return r.WithContext(context.WithValue(r.Context(), userKey{}, s.appUser()))
		}
		return r
	}
	if u := s.cfg.Access.Identify(r, s.requestToken(r)); u != nil {
		return r.WithContext(context.WithValue(r.Context(), userKey{}, u))
	}
	return r
}

// MultiUser reports whether an Access policy is in charge.
func (s *Server) MultiUser() bool { return s.cfg.Access != nil }

// allowed is the one permission check every route uses: always true when
// single-user (the token, if any, was checked already).
func (s *Server) allowed(u *User, act Action, p string) bool {
	if u.IsApp() {
		return appAllows(u.app, act, p)
	}
	if s.cfg.Access == nil {
		return true
	}
	if u == nil {
		return false
	}
	p = path.Clean("/" + p)
	if IsReserved(p) {
		// a version or a trashed file is readable by whoever may read the
		// original; anything else in .myweb only by admins
		if orig := originalOf(p); orig != "" && act == ActRead {
			return s.cfg.Access.Can(u, ActRead, orig)
		}
		return s.cfg.Access.Can(u, ActAdmin, "/")
	}
	return s.cfg.Access.Can(u, act, p)
}

// originalOf maps a stored version or trashed file to the path it belongs to:
//
//	/.myweb/history/notes/a.md/<version> -> /notes/a.md
//	/.myweb/trash/<id>/notes/a.md        -> /notes/a.md
func originalOf(p string) string {
	rest := strings.TrimPrefix(p, "/"+metaDir+"/")
	switch {
	case strings.HasPrefix(rest, "history/"):
		if d := path.Dir(strings.TrimPrefix(rest, "history")); d != "/" && d != "." {
			return d
		}
	case strings.HasPrefix(rest, "trash/"):
		parts := strings.SplitN(strings.TrimPrefix(rest, "trash/"), "/", 2)
		if len(parts) == 2 && parts[1] != "" {
			return "/" + parts[1]
		}
	}
	return ""
}

// visible: may u know that p exists (readable, or a folder on the way to
// something readable)?
func (s *Server) visible(u *User, p string, isDir bool) bool {
	if s.allowed(u, ActRead, p) {
		return true
	}
	if u.IsApp() {
		return isDir && appTraverses(u.app, p)
	}
	return isDir && s.cfg.Access != nil && u != nil && s.cfg.Access.CanTraverse(u, path.Clean("/"+p))
}

func (s *Server) can(r *http.Request, act Action, p string) bool {
	return s.allowed(UserFrom(r.Context()), act, p)
}

func writeForbidden(w http.ResponseWriter, act Action, p string) {
	writeError(w, http.StatusForbidden, "no "+act.String()+" permission for "+path.Clean("/"+p))
}
