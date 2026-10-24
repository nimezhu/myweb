package myserver

// Limiting the web app at "/". An installed app is someone else's code, so
// with Config.App set the requests its pages make get only the app's
// folders. Browsers keep sites apart by origin, so the app and the file
// manager must not share one: the app keeps http://127.0.0.1:PORT/ and the
// file manager moves to http://localhost:PORT/_myweb/.
//
// Which requests are the app's: those from a browser page of this server
// that isn't the file manager (Origin, or Sec-Fetch-Site: same-origin when a
// GET carries no Origin), and those from pages of other sites that aren't
// allowed by CORS. Requests without either header come from programs (curl,
// scripts) or from the user typing an address, and are not limited; nor are
// the file manager's and allowed CORS sites'. Single-user only: with
// Config.Access, people's own permissions apply.

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Folder is a folder of the data folder an app may use.
type Folder struct {
	Path     string `json:"path"`               // e.g. /rx-graphic-editor
	ReadOnly bool   `json:"readOnly,omitempty"` // read, but not write
}

// AppAccess limits the app at "/" to some folders.
type AppAccess struct {
	Name string
	// Folders are what it may use now; called for each request, as they
	// change with updates and grants.
	Folders func() []Folder
}

// ManagerHost is the host name the file manager is served on while the app
// is limited.
const ManagerHost = "localhost"

// appLimited reports whether the app at "/" is limited.
func (s *Server) appLimited() bool { return s.cfg.App != nil && s.cfg.Access == nil }

// fromApp reports whether r comes from the app's pages (see above).
func (s *Server) fromApp(r *http.Request) bool {
	if !s.appLimited() {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "same-origin":
			origin = "http://" + r.Host // a page of the address it asks
		case "same-site", "cross-site":
			return true // some other page: the least it may do
		default:
			return false // a program, or the user typing an address
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true // "null": sandboxed frames, files
	}
	if s.isManagerHost(u.Host) {
		return false
	}
	if u.Host == r.Host {
		return true // a page of this server that isn't the file manager
	}
	return !s.cors.OriginAllowed(&http.Request{Header: http.Header{"Origin": {origin}}})
}

// isManagerHost: host (with port) is the file manager's address.
func (s *Server) isManagerHost(host string) bool {
	h, p, err := net.SplitHostPort(host)
	return err == nil && h == ManagerHost && p == strconv.Itoa(s.port)
}

// appUser is the identity the app's requests get.
func (s *Server) appUser() *User {
	folders := s.cfg.App.Folders()
	home := ""
	for _, f := range folders {
		if !f.ReadOnly {
			home = path.Clean("/" + f.Path)
			break
		}
	}
	return &User{Name: s.cfg.App.Name, Role: "app", Home: home, isApp: true, app: folders}
}

// appAllows: may the app do act on p?
func appAllows(folders []Folder, act Action, p string) bool {
	if act == ActAdmin {
		return false
	}
	p = path.Clean("/" + p)
	if IsReserved(p) {
		// a version or a deleted file of one of its files may be read
		if orig := originalOf(p); orig != "" && act == ActRead {
			return appAllows(folders, act, orig)
		}
		return false
	}
	allowed := false
	for _, f := range folders {
		d := path.Clean("/" + f.Path)
		if d == "/" || p == d || strings.HasPrefix(p, d+"/") {
			if act == ActRead || !f.ReadOnly {
				return true
			}
			allowed = allowed || act == ActRead
		}
	}
	return allowed
}

// appTraverses: is dir on the way to one of the app's folders?
func appTraverses(folders []Folder, dir string) bool {
	dir = path.Clean("/" + dir)
	for _, f := range folders {
		d := path.Clean("/" + f.Path)
		if dir == "/" || strings.HasPrefix(d, dir+"/") {
			return true
		}
	}
	return false
}

// managerOnItsHost sends the file manager's pages to the manager's address
// while the app is limited, and keeps other sites from framing them.
func (s *Server) managerOnItsHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.appLimited() && !s.isManagerHost(r.Host) {
			u := *r.URL
			u.Scheme, u.Host = "http", net.JoinHostPort(ManagerHost, strconv.Itoa(s.port))
			http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

// appOnItsHost sends the app's pages to the app's address: served from the
// file manager's, they would act with its rights.
func (s *Server) appOnItsHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.appLimited() && s.isManagerHost(r.Host) {
			u := *r.URL
			u.Scheme, u.Host = "http", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))
			http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sandboxOnManagerHost: on the file manager's address, a data file that can
// run scripts (an HTML page the app wrote, say) runs them in a sandbox with
// an origin of its own, without the file manager's rights.
func (s *Server) sandboxOnManagerHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.appLimited() && s.isManagerHost(r.Host) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w = &sandboxScripts{ResponseWriter: w}
		}
		next.ServeHTTP(w, r)
	})
}

type sandboxScripts struct {
	http.ResponseWriter
	wrote bool
}

func (s *sandboxScripts) WriteHeader(code int) {
	if !s.wrote {
		s.wrote = true
		if scriptable(s.Header().Get("Content-Type")) {
			s.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms allow-downloads")
		}
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *sandboxScripts) Write(b []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func scriptable(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch ct {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml", "text/xsl":
		return true
	}
	return ct == "" || strings.HasSuffix(ct, "+xml")
}
