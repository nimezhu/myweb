package myserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestCheckHost(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	for _, c := range []struct {
		cfg  Config
		host string
		want int
	}{
		{Config{}, "127.0.0.1:1", 200},
		{Config{}, "localhost:1", 200},
		{Config{}, "app.localhost:1", 200},
		{Config{}, "[::1]:1", 200},
		{Config{}, "evil.example:1", http.StatusMisdirectedRequest}, // DNS rebinding
		{Config{}, "192.168.1.5:1", http.StatusMisdirectedRequest},
		{Config{Global: true}, "lab-server:1", 200},
		{Config{Public: true, Token: "t"}, "x.trycloudflare.com", 401}, // reads need the token
	} {
		c.cfg.Root = root
		ts := httptest.NewServer(New(c.cfg).Handler())
		req, _ := http.NewRequest("GET", ts.URL+"/f/a.txt", nil)
		req.Host = c.host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%+v host %s: %d, want %d", c.cfg, c.host, resp.StatusCode, c.want)
		}
		ts.Close()
	}
}

func testCert(t *testing.T) *tls.Config {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func TestTLS(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("over tls"), 0o644)
	s := New(Config{Root: root, Port: freePort(t), TLS: testCert(t), Token: "tok", Manager: http.FS(fstest.MapFS{"index.html": {Data: []byte("m")}})})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown(t.Context()) })
	port, err := s.Mount(&Mount{Access: &AppAccess{Name: "a", Folders: func() []Folder { return nil }},
		FS: http.FS(fstest.MapFS{"index.html": {Data: []byte("app")}})}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.URL(), "https://") || !strings.HasPrefix(s.mountURL(port), "https://") || !strings.HasPrefix(s.DAVURL(), "https://") {
		t.Errorf("URLs: %s %s %s", s.URL(), s.mountURL(port), s.DAVURL())
	}
	c := http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u string) (*http.Response, string) {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	if _, body := get(s.URL() + "/f/a.txt"); body != "over tls" {
		t.Errorf("file: %q", body)
	}
	if _, body := get("https://127.0.0.1:" + strconv.Itoa(port) + "/"); body != "app" {
		t.Errorf("app port: %q", body)
	}
	resp, _ := get(s.URL() + "/?token=tok")
	if ck := resp.Cookies(); len(ck) != 1 || !ck[0].Secure {
		t.Errorf("sign-in cookie: %+v", ck)
	}
	if resp, _ := http.Get("http://127.0.0.1:" + strconv.Itoa(s.Port()) + "/f/a.txt"); resp != nil && resp.StatusCode == 200 {
		t.Error("plain http answered")
	}
}

func TestPublicOrigin(t *testing.T) {
	s := New(Config{Root: t.TempDir(), Public: true})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", ts.URL+"/w/x.txt", strings.NewReader("x"))
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("https://a-b.trycloudflare.com"); code != 403 {
		t.Errorf("before the tunnel is known: %d", code)
	}
	s.SetPublicURL("https://a-b.trycloudflare.com/")
	if code := post("https://a-b.trycloudflare.com"); code != 200 {
		t.Errorf("a page of the tunnel: %d", code)
	}
	if !strings.HasPrefix(s.ShareURL("x"), "https://a-b.trycloudflare.com/_s/") {
		t.Errorf("share URL %s", s.ShareURL("x"))
	}
}

func TestCertPage(t *testing.T) {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: testCert(t).Certificates[0].Certificate[0]})
	s := New(Config{Root: t.TempDir(), Global: true, Token: "tok", TrustCert: ca})
	h := s.Handler()
	get := func(u string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", u, nil)
		r.Host = "192.168.1.5:8433"
		h.ServeHTTP(w, r)
		return w
	}
	if w := get(CertPath); w.Code != 200 || !strings.Contains(w.Body.String(), "SHA-256") {
		t.Fatalf("page without the token: %d", w.Code)
	}
	if w := get(CertPath + "?download"); w.Code != 200 || w.Body.String() != string(ca) || w.Header().Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Errorf("download: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("/ls/"); w.Code != 401 {
		t.Errorf("the rest still needs the token: %d", w.Code)
	}
	s2 := New(Config{Root: t.TempDir()})
	w := httptest.NewRecorder()
	s2.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1"+CertPath, nil))
	if w.Code == 200 && strings.Contains(w.Body.String(), "SHA-256") {
		t.Error("no CA, no page")
	}
}
