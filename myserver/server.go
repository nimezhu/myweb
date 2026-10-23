package myserver

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/cors"
)

// VERSION is myweb's version: set at build time (-ldflags "-X
// github.com/nimezhu/myweb/myserver.VERSION=0.0.2"), else the release tag it
// was installed from (go install github.com/nimezhu/myweb@v0.0.2), else
// defaultVersion.
var VERSION = defaultVersion

const defaultVersion = "0.0.1"

// releaseTag matches a tag like v0.0.2 or v1.2.0-beta.1, not the
// pseudo-versions Go gives untagged builds (v0.0.0-20261023…-170f780bd079).
var (
	releaseTag   = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)
	pseudoSuffix = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)
)

func init() {
	if VERSION != defaultVersion {
		return // set with -ldflags
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path == "github.com/nimezhu/myweb" &&
		releaseTag.MatchString(bi.Main.Version) && !pseudoSuffix.MatchString(bi.Main.Version) {
		VERSION = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

// DefaultCORS lists the origins allowed to call the API from a browser: the
// mycellome, youdata and cellcycle sites (and their subdomains) and local
// pages. Add others per install with -cors.
var DefaultCORS = []string{
	"https://mycellome.com",
	"https://*.mycellome.com",
	"https://cellcycle.org",
	"https://*.cellcycle.org",
	"https://youdata.studio",
	"https://*.youdata.studio",
	"http://127.0.0.1:*",
	"https://127.0.0.1:*",
	"http://localhost:*",
	"https://localhost:*",
}

// ManagerPath is where the file manager is always available, even when a
// web app is served at "/".
const ManagerPath = "/_myweb/"

// maxPortTries bounds the search for a free port starting at Config.Port.
const maxPortTries = 100

//go:embed all:ctrl
var embededFiles embed.FS

// ClientPath serves client.js: web apps include it to offer a reload when
// myweb installs a new version of them.
const ClientPath = ManagerPath + "client.js"

//go:embed client.js
var clientJS []byte

func getFS() http.FileSystem {
	fsys, err := fs.Sub(embededFiles, "ctrl")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}

// Config describes how the server is set up.
type Config struct {
	Root  string          // root directory of data files
	WebFS http.FileSystem // optional static web app served at "/"
	// Manager, if set, is the file manager page (see package manager),
	// served at ManagerPath, and also at "/" when there is no WebFS.
	Manager http.FileSystem
	// AppServer, if set, enables /io/<app> pages that load the app's
	// main.js and style.css from this base URL.
	AppServer string
	Port      int      // preferred port; the next free one is used if taken
	Global    bool     // listen on all interfaces instead of 127.0.0.1
	ReadOnly  bool     // initial read-only state (can be toggled at runtime)
	CORS      []string // allowed origins

	// Versions and trash (see versions.go). 0 means the default
	// (20 versions, 64 MiB max file size, 30 days); negative turns it off.
	MaxUpload      int64 // per uploaded file; 0 means 100 MiB
	KeepVersions   int
	MaxVersionSize int64
	TrashDays      int

	// Access, if set, makes the server multi-user (see access.go).
	Access Access

	// Token, if set, is required (Bearer header or login cookie) for writes
	// and /cmd/*, and also for reads when Global. Empty disables auth.
	Token string

	// Mux lets callers register extra handlers (plugins). Created if nil.
	Mux *http.ServeMux

	// OnRequest, if set, is called after every request completes.
	OnRequest func(RequestEvent)

	// WebVersion, if set, names the version of WebFS being served; it is sent
	// with each page as Server-Timing "myweb-app", so client.js knows which
	// version a page was loaded from.
	WebVersion func() string

	// Instance, if set, is reported by /cmd/status so a later start can tell
	// that this server is the copy it recorded.
	Instance string
}

// RequestEvent describes a completed request.
type RequestEvent struct {
	Time      time.Time
	Method    string
	Path      string
	Remote    string
	Status    int
	Bytes     int64
	Duration  time.Duration
	Origin    string
	UserAgent string
	Error     string // start of the response body for status >= 400
	User      string // multi-user: who made the request
}

// Server is a running myweb file server.
type Server struct {
	cfg      Config
	srv      *http.Server
	ln       net.Listener
	port     int
	readOnly atomic.Bool

	cmds   cmdRegistry
	locks  pathLocks
	events *eventHub
	cors   *cors.Cors

	proxyClient *http.Client

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan error
}

// New builds a server and registers all routes, but does not listen yet.
func New(cfg Config) *Server {
	if cfg.Root == "" {
		cfg.Root = "."
	}
	if cfg.Port <= 0 {
		cfg.Port = 8433
	}
	if cfg.Mux == nil {
		cfg.Mux = http.NewServeMux()
	}
	s := &Server{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan error, 1),
	}
	s.readOnly.Store(cfg.ReadOnly)
	s.events = newEventHub(s)
	s.proxyClient = newProxyClient(publicOnly)
	s.cors = cors.New(cors.Options{
		AllowedOrigins:   cfg.CORS,
		AllowCredentials: true,
		AllowedMethods:   []string{"GET", "HEAD", "POST", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "If-Match", "If-None-Match",
			"Range", "If-Range", "If-Modified-Since"},
		// let cross-origin apps read the ETag, for conditional writes, and
		// partial-read headers (/f/ and /proxy)
		ExposedHeaders: []string{"ETag", "Content-Range", "Accept-Ranges", "X-Proxy-Final-URL"},
	})
	s.routes()
	s.registerSafetyCmds()
	return s
}

func (s *Server) routes() {
	m := s.cfg.Mux
	root := s.cfg.Root

	m.Handle("/f/", s.authedReads(http.StripPrefix("/f", s.guardFiles(revalidate(root, http.HandlerFunc(s.serveFiles))))))
	m.Handle("/ls/", s.authedReads(http.StripPrefix("/ls", http.HandlerFunc(s.handleLs))))

	// Write routes are always registered and gated at request time so that
	// read-only mode can be toggled while running.
	m.Handle("/w/", http.StripPrefix("/w", s.writable(s.handleWrite)))
	m.Handle("/rm/", http.StripPrefix("/rm", s.writable(s.handleRemove)))
	m.Handle("/cmd/uploadFile", s.writable(s.handleUploadFile))
	m.Handle("/up/", http.StripPrefix("/up", s.writable(s.handleUp)))

	m.Handle("/ctrl/", http.StripPrefix("/ctrl", http.FileServer(getFS())))
	m.HandleFunc(ClientPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(clientJS)
	})
	m.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(VERSION))
	})
	m.HandleFunc("/cmd/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":   1,
			"version":   VERSION,
			"readOnly":  s.ReadOnly(),
			"versions":  s.keepVersions(),
			"trashDays": s.trashDays(),
			"auth":      s.AuthEnabled(),
			"authed":    s.Authorized(r),
			"user":      UserFrom(r.Context()),
			"multiUser": s.MultiUser(),
			"instance":  s.cfg.Instance,
		})
	})
	m.HandleFunc("/events", s.serveEvents)
	m.HandleFunc("/echo", s.echo)
	m.HandleFunc("/echotest", echoHome)
	m.Handle("/cmd/fetchUrl", s.authed(http.HandlerFunc(FetchURLHandleFunc)))
	m.Handle("/proxy", s.authed(http.HandlerFunc(s.handleProxy)))
	m.HandleFunc("/cmd/", s.serveCmd)
	m.HandleFunc("/cmd/stopServer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Post data only", http.StatusMethodNotAllowed)
			return
		}
		// CORS does not stop "simple" cross-origin POSTs, so without this any
		// web page could shut the server down. Only the server's own pages may.
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "stopServer is only allowed from this server's own pages")
			return
		}
		if !s.Authorized(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		if !s.can(r, ActAdmin, "/") {
			writeForbidden(w, ActAdmin, "/")
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"success": 1})
		s.RequestStop()
	})

	if s.cfg.AppServer != "" {
		m.Handle("/io/", http.StripPrefix("/io", appHandler(s.cfg.AppServer)))
	}
	if s.cfg.Manager != nil {
		m.Handle(ManagerPath, noCache(http.StripPrefix(strings.TrimSuffix(ManagerPath, "/"), http.FileServer(s.cfg.Manager))))
	}
	// no-cache: after an app update (or an edit with -web) browsers must
	// check before reusing a cached index.html or script
	switch {
	case s.cfg.WebFS != nil:
		m.Handle("/", noCache(s.webVersion(http.FileServer(s.cfg.WebFS))))
	case s.cfg.Manager != nil:
		m.Handle("/", noCache(http.FileServer(s.cfg.Manager)))
	}
}

func (s *Server) writable(h http.HandlerFunc) http.Handler {
	return s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.readOnly.Load() {
			writeError(w, http.StatusForbidden, "server is in read-only mode")
			return
		}
		h(w, r)
	}))
}

func (s *Server) handler() http.Handler {
	return recoverer(s.cors.Handler(s.logRequests(s.login(s.cfg.Mux))))
}

func (s *Server) host() string {
	if s.cfg.Global {
		return ""
	}
	return "127.0.0.1"
}

// listen binds the preferred port or the next free one. Binding directly
// (instead of probing first) avoids a race with other processes.
func (s *Server) listen() (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < maxPortTries; i++ {
		port := s.cfg.Port + i
		ln, err := net.Listen("tcp", net.JoinHostPort(s.host(), strconv.Itoa(port)))
		if err == nil {
			return ln, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("no free port in %d-%d: %w", s.cfg.Port, s.cfg.Port+maxPortTries-1, lastErr)
}

// Start binds a port and serves in the background. Errors binding the port
// are returned; later serve errors arrive on Done.
func (s *Server) Start() error {
	ln, port, err := s.listen()
	if err != nil {
		return err
	}
	s.ln = ln
	s.port = port
	go s.pruneTrash()
	s.srv = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.Default(),
	}
	go func() {
		err := s.srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.doneCh <- err
	}()
	return nil
}

// Shutdown gracefully stops the server, waiting for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	s.events.close() // WebSockets are hijacked: http.Server.Shutdown doesn't close them
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// Done receives the serve error (nil on clean shutdown) once serving stops.
func (s *Server) Done() <-chan error { return s.doneCh }

// StopRequested is closed when a client calls /cmd/stopServer.
func (s *Server) StopRequested() <-chan struct{} { return s.stopCh }

// RequestStop signals that the server should be stopped.
func (s *Server) RequestStop() { s.stopOnce.Do(func() { close(s.stopCh) }) }

func (s *Server) Port() int           { return s.port }
func (s *Server) Root() string        { return s.cfg.Root }
func (s *Server) Global() bool        { return s.cfg.Global }
func (s *Server) ReadOnly() bool      { return s.readOnly.Load() }
func (s *Server) Mux() *http.ServeMux { return s.cfg.Mux }
func (s *Server) URL() string         { return s.urlFor("127.0.0.1") }
func (s *Server) urlFor(h string) string {
	return "http://" + net.JoinHostPort(h, strconv.Itoa(s.port))
}

// LANURLs lists URLs reachable from other machines (only when Global).
func (s *Server) LANURLs() []string {
	if !s.cfg.Global {
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var urls []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		urls = append(urls, s.urlFor(ipnet.IP.String()))
	}
	return urls
}

// sameOrigin reports whether a browser request came from a page served by
// this server. Requests without an Origin header (curl, scripts) are allowed.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// ResolvePath maps a URL-style path ("/a/b.txt") to a filesystem path inside
// Root. ".." cannot escape Root. Plugins should use this for every path.
func (s *Server) ResolvePath(urlPath string) string {
	p, _, err := safeJoin(s.cfg.Root, urlPath)
	if err == errRootPath {
		return filepath.Clean(s.cfg.Root)
	}
	return p
}

// SetReadOnly switches read-only mode and tells /events clients.
func (s *Server) SetReadOnly(v bool) {
	if s.readOnly.Swap(v) != v {
		s.events.broadcast(map[string]interface{}{"type": "status", "readOnly": v})
	}
}

// ManagerURL is the file manager's address ("" without one); it is the same
// page as URL() when no web app is mounted at "/".
func (s *Server) ManagerURL() string {
	if s.cfg.Manager == nil {
		return ""
	}
	return s.URL() + ManagerPath
}

// ManagerAtRoot reports whether "/" shows the file manager (no web app).
func (s *Server) ManagerAtRoot() bool { return s.cfg.Manager != nil && s.cfg.WebFS == nil }

// ManagerLoginURL opens the file manager signed in (when auth is on).
func (s *Server) ManagerLoginURL() string {
	u := s.ManagerURL()
	if u != "" && s.cfg.Token != "" {
		u += "?token=" + s.cfg.Token
	}
	return u
}

// ServesRoot reports whether anything (a web app or the manager) is at "/".
func (s *Server) ServesRoot() bool { return s.cfg.WebFS != nil || s.cfg.Manager != nil }

// Broadcast sends a JSON message to every /events client, e.g.
// {"type":"app-updated","version":"1.4.2"}.
func (s *Server) Broadcast(v interface{}) { s.events.broadcast(v) }

func (s *Server) webVersion(h http.Handler) http.Handler {
	if s.cfg.WebVersion == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := s.cfg.WebVersion(); v != "" {
			w.Header().Set("Server-Timing", `myweb-app;desc="`+strings.Trim(strconv.Quote(v), `"`)+`"`)
		}
		h.ServeHTTP(w, r)
	})
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
