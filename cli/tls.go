package cli

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
)

// HTTPS: -https serves with a certificate made here, signed by a small CA
// of the home's own (both kept in <home>/tls/); -cert and -key serve one you
// have (mkcert, tailscale cert, a CA). The CA is what other machines trust
// (myweb tls trust, or /_myweb/cert): it lasts ten years, so the server's
// certificate can be made again for new addresses without trusting anew.
// It may sign only local names (localhost, .local, .lan, .internal,
// home.arpa and this machine's), so its key can't stand in for websites.

// certValidity is how long a made certificate lasts; it is made again a
// month before it ends, or when this machine gets a new name or address.
const (
	certValidity = 397 * 24 * time.Hour
	caValidity   = 10 * 365 * 24 * time.Hour
)

// localDomains are the names the CA may sign, with this machine's name.
var localDomains = []string{"localhost", "local", "lan", "internal", "home.arpa"}

func tlsDir(dirs home.Dirs) string { return filepath.Join(dirs.Root, "tls") }

// caFile is the CA certificate to trust (PEM).
func caFile(dirs home.Dirs) string { return filepath.Join(tlsDir(dirs), "ca.pem") }

// madeTLS reports what loadTLS made.
type madeTLS struct{ ca, cert bool }

// loadTLS returns the server's TLS config and the certificate's SHA-256
// fingerprint (hex), from certFile and keyFile, or else the home's own.
func loadTLS(dirs home.Dirs, certFile, keyFile string, global bool) (*tls.Config, string, madeTLS, error) {
	var made madeTLS
	if certFile == "" {
		names := certNames(global)
		ca, caKey, err := loadCA(dirs)
		if err != nil || caNeeded(ca, names) {
			if ca, caKey, err = makeCA(dirs, names); err != nil {
				return nil, "", made, fmt.Errorf("making a CA: %w", err)
			}
			made.ca = true
		}
		certFile, keyFile = filepath.Join(tlsDir(dirs), "cert.pem"), filepath.Join(tlsDir(dirs), "key.pem")
		if certNeeded(certFile, names, ca) {
			if err := makeCert(certFile, keyFile, names, ca, caKey); err != nil {
				return nil, "", made, fmt.Errorf("making a certificate: %w", err)
			}
			made.cert = true
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, "", made, err
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, hex.EncodeToString(sum[:]), made, nil
}

// certNames are the names and addresses a made certificate covers: this
// machine's, and with -global its network addresses and host name.
func certNames(global bool) []string {
	names := []string{"localhost", "127.0.0.1", "::1"}
	if !global {
		return names
	}
	if h := hostName(); h != "" {
		names = append(names, h)
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				names = append(names, ipn.IP.String())
			}
		}
	}
	return names
}

func hostName() string {
	h, _ := os.Hostname()
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func readCert(file string) (*x509.Certificate, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New(file + ": no certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func loadCA(dirs home.Dirs) (*x509.Certificate, crypto.Signer, error) {
	ca, err := readCert(caFile(dirs))
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(filepath.Join(tlsDir(dirs), "ca-key.pem"))
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, nil, errors.New("ca-key.pem: no key")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return ca, key, nil
}

// caNeeded reports whether ca is missing, ends within a month, or may not
// sign one of names.
func caNeeded(ca *x509.Certificate, names []string) bool {
	if ca == nil || time.Until(ca.NotAfter) < 30*24*time.Hour {
		return true
	}
	for _, n := range names {
		if net.ParseIP(n) == nil && !slices.ContainsFunc(ca.PermittedDNSDomains, func(d string) bool { return n == d || strings.HasSuffix(n, "."+d) }) {
			return true
		}
	}
	return false
}

// makeCA makes the home's CA, for local names and this machine's.
func makeCA(dirs home.Dirs, names []string) (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	permitted := slices.Clone(localDomains)
	for _, n := range append(names, hostName()) {
		if n != "" && net.ParseIP(n) == nil && !slices.ContainsFunc(permitted, func(d string) bool { return n == d || strings.HasSuffix(n, "."+d) }) {
			permitted = append(permitted, n)
		}
	}
	who := hostName()
	if u := os.Getenv("USER"); u != "" {
		who = u + "@" + who
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "myweb local CA (" + who + ")", Organization: []string{"myweb"}},
		NotBefore:                   time.Now().Add(-time.Hour),
		NotAfter:                    time.Now().Add(caValidity),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permitted,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKeyPair(caFile(dirs), filepath.Join(tlsDir(dirs), "ca-key.pem"), der, key); err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(der)
	return ca, key, err
}

// certNeeded reports whether the certificate at file is missing, ends
// within a month, isn't signed by ca, or doesn't cover every one of names.
func certNeeded(file string, names []string, ca *x509.Certificate) bool {
	c, err := readCert(file)
	if err != nil || time.Until(c.NotAfter) < 30*24*time.Hour {
		return true
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := c.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
		return true
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			if !slices.ContainsFunc(c.IPAddresses, ip.Equal) {
				return true
			}
		} else if !slices.Contains(c.DNSNames, n) {
			return true
		}
	}
	return false
}

func makeCert(certFile, keyFile string, names []string, ca *x509.Certificate, caKey crypto.Signer) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "myweb on " + hostName()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	return writeKeyPair(certFile, keyFile, der, key)
}

func writeKeyPair(certFile, keyFile string, der []byte, key *ecdsa.PrivateKey) error {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return writeFileAtomic(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeFileAtomic(file string, b []byte, mode os.FileMode) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// fingerprint formats a hex SHA-256 as browsers show it: AB:CD:…
func fingerprint(h string) string {
	h = strings.ToUpper(h)
	var parts []string
	for i := 0; i+2 <= len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// pinnedTLS trusts only the certificate whose SHA-256 is sum (hex): how this
// program's commands talk to its own HTTPS server on 127.0.0.1.
func pinnedTLS(sum string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // no CA to ask: the pin below is the check
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			got := sha256.Sum256(raw[0])
			if hex.EncodeToString(got[:]) != sum {
				return errors.New("not the certificate of the server that was started")
			}
			return nil
		},
	}
}
