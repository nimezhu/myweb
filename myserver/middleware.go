package myserver

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// statusRecorder captures status and size while staying usable for
// websocket upgrades (Hijacker) and streaming (Flusher).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	errBuf []byte // start of an error response, for the request log
}

const maxErrBody = 300

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	if r.status >= 400 && len(r.errBuf) < maxErrBody {
		r.errBuf = append(r.errBuf, b[:min(n, maxErrBody-len(r.errBuf))]...)
	}
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack not supported")
	}
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		r = s.identify(r) // multi-user: who is this, for every handler below
		next.ServeHTTP(rec, r)
		if s.cfg.OnRequest == nil || isProbe(r) {
			return
		}
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.cfg.OnRequest(RequestEvent{
			Time:      start,
			Method:    r.Method,
			Path:      redactShare(r.URL.RequestURI()),
			Remote:    r.RemoteAddr,
			Status:    rec.status,
			Bytes:     rec.bytes,
			Duration:  time.Since(start),
			Origin:    r.Header.Get("Origin"),
			UserAgent: r.UserAgent(),
			Error:     string(rec.errBuf),
			User:      userName(UserFrom(r.Context())),
		})
	})
}

func isProbe(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/cmd/status" && r.Header.Get(ProbeHeader) != ""
}

// recoverer keeps one bad request from taking down the whole server.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func userName(u *User) string {
	if u == nil {
		return ""
	}
	return u.Name
}

// guardWrites refuses requests that can change something (anything but
// GET, HEAD and OPTIONS) from web pages of other sites. CORS alone doesn't:
// browsers send a "simple" cross-site POST (a form, or text/plain) without
// asking first, so without this any page could write to the data folder of a
// server without a token. Allowed: the server's own pages, the -cors origins,
// and clients that aren't browsers (no Origin header).
// checkHost answers only requests sent to this machine's own names, unless
// it is reachable from elsewhere (-global, a tunnel), where reads need the
// token. A web page whose name an attacker points at 127.0.0.1 (DNS
// rebinding) would otherwise be the same site as this server, and read it.
func (s *Server) checkHost(next http.Handler) http.Handler {
	if s.exposed() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(requestHostname(r)) {
			writeError(w, http.StatusMisdirectedRequest, "this server answers only to localhost and 127.0.0.1 (start it with -global for other names)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// localHost reports whether h names this machine: localhost, *.localhost,
// or a loopback address.
func localHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) guardWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.originAllowed(r) {
				writeError(w, http.StatusForbidden, "requests from other websites may not change anything here (allow a site with -cors)")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(r *http.Request) bool {
	if r.Header.Get("Origin") == "" {
		// browsers send Origin on every cross-site POST; Sec-Fetch-Site
		// catches the rare one that doesn't
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	if u := s.PublicURL(); u != "" && r.Header.Get("Origin") == u {
		return true // a page from the tunnel's address
	}
	return sameOrigin(r) || s.cors.OriginAllowed(r)
}
