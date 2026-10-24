package cli

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nimezhu/myweb/home"
)

func TestTunnel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script plays cloudflared")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "cloudflared")
	os.WriteFile(fake, []byte(`#!/bin/sh
echo "$*" > "`+dir+`/args"
echo "INF |  https://a-b-c.trycloudflare.com  |" >&2
exec sleep 30
`), 0o755)
	tunnelCommand = fake
	defer func() { tunnelCommand = "cloudflared" }()
	got := make(chan string, 2)
	tun, err := startTunnel("https://127.0.0.1:9", func(u string) { got <- u })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-got:
		if u != "https://a-b-c.trycloudflare.com" {
			t.Fatalf("url %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no address")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "args")); string(b) != "tunnel --no-autoupdate --url https://127.0.0.1:9 --no-tls-verify\n" {
		t.Errorf("args %q", b)
	}
	tun.stop()
	select {
	case u := <-got:
		if u != "" {
			t.Errorf("after stop: %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop didn't end it")
	}
	tunnelCommand = filepath.Join(dir, "nope")
	if _, err := startTunnel("http://x", func(string) {}); err == nil {
		t.Error("a missing cloudflared should be an error")
	}
}

func TestMadeCert(t *testing.T) {
	dirs := homeDirs(t)
	conf, sum, made, err := loadTLS(dirs, "", "", false)
	if err != nil || !made.ca || !made.cert || len(sum) != 64 || len(conf.Certificates) != 1 {
		t.Fatalf("%v %q %v", err, sum, made)
	}
	_, sum2, made2, _ := loadTLS(dirs, "", "", false)
	if made2.ca || made2.cert || sum2 != sum {
		t.Error("the certificate should be kept")
	}
	for _, k := range []string{"key.pem", "ca-key.pem"} {
		if fi, _ := os.Stat(filepath.Join(tlsDir(dirs), k)); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", k, fi.Mode().Perm())
		}
	}
	ca, _ := readCert(caFile(dirs))
	if !ca.IsCA || !slices.Contains(ca.PermittedDNSDomains, "local") || !ca.PermittedDNSDomainsCritical {
		t.Errorf("CA %v %v", ca.IsCA, ca.PermittedDNSDomains)
	}
	// the server's certificate chains to the CA, for its names only
	leaf, _ := x509.ParseCertificate(conf.Certificates[0].Certificate[0])
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost"}); err != nil {
		t.Error(err)
	}
	if !certNeeded(filepath.Join(tlsDir(dirs), "cert.pem"), []string{"localhost", "10.9.8.7"}, ca) {
		t.Error("a new address needs a new certificate")
	}
	// a new address: a new certificate, the same CA (nothing to trust anew)
	if caNeeded(ca, []string{"localhost", "box.lan", "10.9.8.7"}) {
		t.Error("the CA covers .lan names and any address")
	}
	// the CA's key can't sign for a website: what it signs isn't valid
	_, caKey, _ := loadCA(dirs)
	other := filepath.Join(t.TempDir(), "c.pem")
	if err := makeCert(other, other+".key", []string{"example.com"}, ca, caKey); err != nil {
		t.Fatal(err)
	}
	c, _ := readCert(other)
	if _, err := c.Verify(x509.VerifyOptions{Roots: roots, DNSName: "example.com"}); err == nil {
		t.Error("a certificate for example.com signed by the CA is valid")
	}
	if !caNeeded(ca, []string{"example.com"}) {
		t.Error("a name the CA may not sign needs a new CA")
	}
}

func TestTrustCommands(t *testing.T) {
	dirs := homeDirs(t)
	loadTLS(dirs, "", "", false)
	ca, _ := readCert(caFile(dirs))
	yes := func(string) bool { return true }
	debian := func(p string) bool {
		return p == "/usr/local/share/ca-certificates" || strings.HasSuffix(p, ".pki/nssdb")
	}
	for _, tc := range []struct {
		goos string
		add  bool
		want []string
	}{
		{"darwin", true, []string{"security add-trusted-cert -r trustRoot -k /h/Library/Keychains/login.keychain-db"}},
		{"darwin", false, []string{"security remove-trusted-cert", "security delete-certificate -Z"}},
		{"windows", true, []string{"certutil -user -addstore Root"}},
		{"windows", false, []string{"certutil -user -delstore Root"}},
		{"linux", true, []string{"install -m 0644", "update-ca-certificates", "certutil -d sql:/h/.pki/nssdb -A -t C,,"}},
		{"linux", false, []string{"rm -f /usr/local/share/ca-certificates/myweb-", "update-ca-certificates --fresh", "certutil -d sql:/h/.pki/nssdb -D"}},
	} {
		cmds, _, err := trustCommands(tc.goos, caFile(dirs), ca, tc.add, "/h", yes, debian)
		if err != nil || len(cmds) != len(tc.want) {
			t.Fatalf("%s %v: %v %q", tc.goos, tc.add, err, cmds)
		}
		for i, w := range tc.want {
			if got := shellQuote(cmds[i]); !strings.Contains(got, w) {
				t.Errorf("%s %v: %q lacks %q", tc.goos, tc.add, got, w)
			}
		}
	}
	if _, _, err := trustCommands("linux", caFile(dirs), ca, true, "/h", yes, func(string) bool { return false }); err == nil {
		t.Error("no known store: an error")
	}
	_, notes, _ := trustCommands("linux", caFile(dirs), ca, true, "/h", func(c string) bool { return c != "certutil" }, debian)
	if len(notes) != 2 || !strings.Contains(notes[0], "certutil") {
		t.Errorf("notes %q", notes)
	}
}

func homeDirs(t *testing.T) home.Dirs { return home.Dirs{Root: t.TempDir()} }
