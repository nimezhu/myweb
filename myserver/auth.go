package myserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// NewToken returns a random 128-bit hex token.
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// AuthEnabled reports whether requests must carry a token: the server's
// token, or (multi-user) a personal one.
func (s *Server) AuthEnabled() bool { return s.cfg.Token != "" || s.cfg.Access != nil }

// Token is the access token ("" when auth is disabled).
func (s *Server) Token() string { return s.cfg.Token }

// LoginURL opens the web app and hands the browser the token as a cookie.
func (s *Server) LoginURL() string {
	if s.cfg.Token == "" { // no token, or personal links (multi-user)
		return s.URL()
	}
	return s.URL() + "/?token=" + s.cfg.Token
}

// cookieName includes the port: cookies are not port-scoped, so two servers
// on 127.0.0.1 would otherwise overwrite each other's token.
func (s *Server) cookieName() string { return "myweb_token_" + strconv.Itoa(s.port) }

func (s *Server) tokenOK(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
}

// Authorized reports whether r carries the token via the
// "Authorization: Bearer" header or the login cookie.
func (s *Server) Authorized(r *http.Request) bool {
	if s.cfg.Access != nil { // multi-user: signed in as someone
		return UserFrom(r.Context()) != nil
	}
	if !s.AuthEnabled() {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return s.tokenOK(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie(s.cookieName()); err == nil {
		return s.tokenOK(c.Value)
	}
	return false
}

// authed rejects requests without the token.
func (s *Server) authed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Authorized(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// authedReads protects read endpoints when the server is network-visible,
// and always when multi-user.
func (s *Server) authedReads(h http.Handler) http.Handler {
	if !s.cfg.Global && s.cfg.Access == nil {
		return h
	}
	return s.authed(h)
}

// login turns a valid "?token=" on a GET into a cookie and redirects to the
// same URL without the token, so it doesn't linger in history or Referer.
func (s *Server) login(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		t := q.Get("token")
		if t == "" || r.Method != http.MethodGet || !s.AuthEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		ok := s.tokenOK(t)
		if s.cfg.Access != nil { // a personal sign-in link
			ok = s.cfg.Access.Identify(r, t) != nil
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     s.cookieName(),
			Value:    t,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		q.Del("token")
		u := *r.URL
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
	})
}
