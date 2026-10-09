package myserver

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// Headers passed from the browser to the remote site; everything else
// (cookies, Authorization with our token, …) stays behind.
var proxyRequestHeaders = []string{
	"Accept", "Accept-Language", "Range", "If-Range",
	"If-None-Match", "If-Modified-Since",
}

// Headers passed back from the remote site; no Set-Cookie, no CORS headers
// (ours apply).
var proxyResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Content-Disposition", "Content-Language",
	"ETag", "Last-Modified", "Cache-Control", "Expires",
}

var errPrivateAddr = errors.New("address is not public")

// publicOnly refuses connections to this machine and the local network
// (loopback, private, link-local, …), so the proxy can't be used to reach
// them. It runs on the address actually dialed: redirects and DNS answers
// can't get around it.
func publicOnly(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	ip := ap.Addr().Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip) { // carrier-grade NAT
		return fmt.Errorf("%s: %w", ip, errPrivateAddr)
	}
	return nil
}

func newProxyClient(control func(string, string, syscall.RawConn) error) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: control}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // the address check must see the real destination
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		},
		// no overall timeout: large files stream for as long as they take,
		// and stop when the browser goes away
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http URL")
			}
			return nil
		},
	}
}

// handleProxy fetches ?url= and streams the answer back, so browser apps can
// read sites that don't send CORS headers. GET and HEAD only.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "GET or HEAD only")
		return
	}
	raw := r.URL.Query().Get("url")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	for _, h := range proxyRequestHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	req.Header.Set("User-Agent", "myweb/"+VERSION)

	resp, err := s.proxyClient.Do(req)
	if err != nil {
		if errors.Is(err, errPrivateAddr) {
			writeError(w, http.StatusForbidden, "proxy only reaches public addresses")
			return
		}
		writeError(w, http.StatusBadGateway, "fetch failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	h := w.Header()
	for _, k := range proxyResponseHeaders {
		if v := resp.Header.Values(k); len(v) > 0 {
			h[k] = v
		}
	}
	// The answer is served from our origin, where the login cookie and the
	// file API live: never let a proxied page run scripts here.
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Proxy-Final-URL", resp.Request.URL.String()) // after redirects
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		io.Copy(w, resp.Body)
	}
}
