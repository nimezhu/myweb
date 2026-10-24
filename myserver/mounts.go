package myserver

// Apps on ports of their own. Besides the app at "/", a server can serve
// more apps, each on a port of its own, so the browser keeps them apart
// (and apart from the file manager): http://127.0.0.1:PORT/ is the app's
// pages and the same API as the main port, limited to the app's folders.
// http://<main address>/apps/NAME/ redirects there. Multi-user (myweb-team),
// the person signed in uses the app with their own rights, within its
// folders.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mount is an app to serve on a port of its own.
type Mount struct {
	Access  *AppAccess      // its name and folders
	FS      http.FileSystem // its pages
	Version func() string   // as Config.WebVersion; may be nil
}

type mounted struct {
	*Mount
	port int
	srv  *http.Server
}

// MountInfo describes a mounted app.
type MountInfo struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

// Mount serves m on port, or when that is taken (or 0) on a free port above
// the main one, and returns the port. Mounting a name again returns its port.
func (s *Server) Mount(m *Mount, port int) (int, error) {
	if m.Access == nil || m.FS == nil {
		return 0, errors.New("mount: Access and FS are required")
	}
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	for p, x := range s.mounts {
		if x.Access.Name == m.Access.Name {
			return p, nil
		}
	}
	ln, err := s.listenMount(port)
	if err != nil {
		return 0, err
	}
	p := ln.Addr().(*net.TCPAddr).Port
	if s.cfg.TLS != nil {
		ln = tls.NewListener(ln, s.cfg.TLS)
	}
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.Default(),
	}
	if s.mounts == nil {
		s.mounts = map[int]*mounted{}
	}
	s.mounts[p] = &mounted{Mount: m, port: p, srv: srv}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("%s: %v", m.Access.Name, err)
		}
	}()
	return p, nil
}

// listenMount binds port, else the first free one above the main port that
// no other app has.
func (s *Server) listenMount(port int) (net.Listener, error) {
	if port > 0 && s.mounts[port] == nil && port != s.port {
		if ln, err := net.Listen("tcp", net.JoinHostPort(s.host(), strconv.Itoa(port))); err == nil {
			return ln, nil
		}
	}
	var lastErr error
	for p := s.port + 1; p < s.port+1+maxPortTries; p++ {
		if s.mounts[p] != nil {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(s.host(), strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no free port for the app: %w", lastErr)
}

// Unmount stops serving an app.
func (s *Server) Unmount(name string) {
	s.mountMu.Lock()
	var m *mounted
	for p, x := range s.mounts {
		if x.Access.Name == name {
			m = x
			delete(s.mounts, p)
		}
	}
	s.mountMu.Unlock()
	if m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.srv.Shutdown(ctx)
	}
}

func (s *Server) unmountAll(ctx context.Context) {
	s.mountMu.Lock()
	ms := s.mounts
	s.mounts = nil
	s.mountMu.Unlock()
	for _, m := range ms {
		m.srv.Shutdown(ctx)
	}
}

// Mounted lists the mounted apps by name.
func (s *Server) Mounted() []MountInfo {
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	var out []MountInfo
	for p, m := range s.mounts {
		out = append(out, MountInfo{m.Access.Name, p, s.mountURL(p)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MountURL is a mounted app's address ("" if it isn't mounted), with the
// sign-in token when auth is on.
func (s *Server) MountURL(name string) string {
	if m := s.mountNamed(name); m != nil {
		u := s.mountURL(m.port)
		if s.cfg.Token != "" {
			u += "?token=" + s.cfg.Token
		}
		return u
	}
	return ""
}

func (s *Server) mountURL(port int) string {
	host := "127.0.0.1"
	if lan := s.LANURLs(); len(lan) > 0 { // -global: as other machines reach it
		if u, err := url.Parse(lan[0]); err == nil {
			host = u.Hostname()
		}
	}
	return s.Scheme() + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

func (s *Server) mountCount() int {
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	return len(s.mounts)
}

// mountFor is the app on the port of host (a page's origin), if any.
func (s *Server) mountFor(host string) *mounted {
	_, p, err := net.SplitHostPort(host)
	if err != nil {
		return nil
	}
	port, _ := strconv.Atoi(p)
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	return s.mounts[port]
}

// mountAt is the app on the port r arrived on, if any.
func (s *Server) mountAt(r *http.Request) *mounted {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return nil
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return nil
	}
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	return s.mounts[tcp.Port]
}

func (s *Server) mountNamed(name string) *mounted {
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	for _, m := range s.mounts {
		if m.Access.Name == name {
			return m
		}
	}
	return nil
}

// serveRoot serves "/": a mounted app's pages on its port; /apps/NAME/
// redirects to a mounted app; else main (the app at "/" or the file manager).
func (s *Server) serveRoot(main http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := s.mountAt(r); m != nil {
			if m.Version != nil {
				if v := m.Version(); v != "" {
					w.Header().Set("Server-Timing", `myweb-app;desc="`+strings.Trim(strconv.Quote(v), `"`)+`"`)
				}
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.FileServer(m.FS).ServeHTTP(w, r)
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, "/apps/"); ok {
			name, sub, _ := strings.Cut(rest, "/")
			if m := s.mountNamed(name); m != nil {
				target := s.Scheme() + "://" + net.JoinHostPort(requestHostname(r), strconv.Itoa(m.port)) + "/" + sub
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				return
			}
		}
		if main == nil {
			http.NotFound(w, r)
			return
		}
		main.ServeHTTP(w, r)
	})
}
