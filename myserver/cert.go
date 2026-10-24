package myserver

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// CertPath offers the CA that signed the server's certificate (Config.
// TrustCert), for other devices to trust: a page with the steps, and the
// certificate itself at ?download. Anyone may fetch it, without the token:
// it is public, as every TLS handshake sends what it signed.
const CertPath = "/_myweb/cert"

func (s *Server) serveCert(w http.ResponseWriter, r *http.Request) {
	block, _ := pem.Decode(s.cfg.TrustCert)
	if block == nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="myweb-ca.crt"`)
		w.Write(s.cfg.TrustCert)
		return
	}
	name := "myweb local CA"
	if c, err := x509.ParseCertificate(block.Bytes); err == nil {
		name = c.Subject.CommonName
	}
	sum := sha256.Sum256(block.Bytes)
	fp := strings.ToUpper(fmt.Sprintf("% x", sum[:]))
	fp = strings.ReplaceAll(fp, " ", ":")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	fmt.Fprintf(w, certPage, html.EscapeString(name), fp, CertPath+"?download")
}

const certPage = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Trust this myweb</title>
<style>body{font:15px/1.55 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 16px;color:#222;background:#fff}
code{font-size:.85em}.fp code{word-break:break-all}a.btn{display:inline-block;background:#5b3fd4;color:#fff;padding:.5em 1em;border-radius:6px;text-decoration:none}
h2{font-size:1rem;margin:1.4em 0 .2em}p,li{margin:.3em 0}.fp{background:#f4f4f6;padding:.6em;border-radius:6px}
@media (prefers-color-scheme:dark){body{background:#16161a;color:#ddd}.fp{background:#25252b}a{color:#b5a6ff}}</style></head><body>
<h1>Trust this myweb</h1>
<p>This server's HTTPS certificate is signed by <b>%[1]s</b>. Once a device trusts it, browsers there open this server without a warning.
It can sign only local names (localhost, .local, .lan, .internal, home.arpa and the machine's own name), not websites.</p>
<p class="fp">SHA-256 <code>%[2]s</code><br><small>Check it matches what <code>myweb tls</code> prints on the server's machine before you trust it.</small></p>
<p><a class="btn" href="%[3]s">Download the certificate</a></p>
<h2>macOS</h2><p>Open the file: it goes to Keychain Access. Double-click it there, open <i>Trust</i>, and set <i>When using this certificate</i> to <i>Always Trust</i>.</p>
<h2>Windows</h2><p>Open the file, <i>Install Certificate…</i>, <i>Current User</i>, <i>Place all certificates in the following store</i>: <i>Trusted Root Certification Authorities</i>.</p>
<h2>iPhone and iPad</h2><p>Download it in Safari and allow the profile; install it in Settings › General › VPN &amp; Device Management; then turn it on in Settings › General › About › Certificate Trust Settings.</p>
<h2>Android</h2><p>Settings › Security › Encryption &amp; credentials › Install a certificate › CA certificate, and choose the file.</p>
<h2>Linux, and Firefox anywhere</h2><p>On the machine that runs myweb: <code>myweb tls trust</code>. Firefox keeps its own list: Settings › Privacy &amp; Security › Certificates › View Certificates › Authorities › Import.</p>
</body></html>
`
