// Package webapp installs and updates the web app myweb serves at "/" from a
// distribution server (-web-from), safely:
//
//   - The server publishes a manifest (e.g. stable.json) and its ed25519
//     signature (stable.json.sig). The manifest names the version and the
//     SHA-256 of a zip with the app's files.
//   - myweb verifies the signature with a public key it is given (-web-key),
//     so a compromised server cannot push code; then the zip's hash.
//   - Each version is unpacked into its own folder and the current one is
//     switched in a single step; previous versions are kept for rollback.
//   - It never moves to an older version by itself, and keeps serving the
//     installed version when the server can't be reached.
//
// cmd/myweb-publish creates keys, bundles and signed manifests.
package webapp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Manifest describes one published version of an app.
type Manifest struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	URL       string    `json:"url"` // the zip; may be relative to the manifest
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Requires  Requires  `json:"requires,omitempty"`
	Published time.Time `json:"published"`
	Notes     string    `json:"notes,omitempty"`
}

// Requires lists what the app needs from the server.
type Requires struct {
	Myweb string `json:"myweb,omitempty"` // e.g. ">=0.2.0"
}

var (
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	versionRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}([-+][0-9A-Za-z.-]+)?$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate checks the fields myweb relies on before installing.
func (m *Manifest) Validate() error {
	switch {
	case !nameRe.MatchString(m.Name):
		return fmt.Errorf("manifest: invalid name %q", m.Name)
	case !versionRe.MatchString(m.Version):
		return fmt.Errorf("manifest: invalid version %q", m.Version)
	case !sha256Re.MatchString(m.SHA256):
		return errors.New("manifest: sha256 must be 64 lowercase hex digits")
	case m.URL == "":
		return errors.New("manifest: url is required")
	case m.Size < 0:
		return errors.New("manifest: invalid size")
	}
	return nil
}

// ---- keys and signatures ----

// GenerateKey returns a new key pair, encoded for files and flags.
func GenerateKey() (public, private string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return EncodePublicKey(pub), base64.StdEncoding.EncodeToString(priv.Seed()), nil
}

// ParsePublicKey decodes a base64 ed25519 public key (as printed by keygen).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "ed25519:")))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("public key must be a base64 ed25519 key (32 bytes)")
	}
	return ed25519.PublicKey(b), nil
}

// LoadPublicKey accepts a public key either as printed by keygen or as the
// path of a file holding it (e.g. app.key.pub).
func LoadPublicKey(s string) (ed25519.PublicKey, error) {
	if pub, err := ParsePublicKey(s); err == nil {
		return pub, nil
	}
	b, err := os.ReadFile(strings.TrimSpace(s))
	if err != nil {
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("a public key is required")
		}
		return nil, fmt.Errorf("%q is neither a base64 ed25519 public key nor a readable key file", s)
	}
	pub, err := ParsePublicKey(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", strings.TrimSpace(s), err)
	}
	return pub, nil
}

// EncodePublicKey renders a public key the way keygen prints it.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ParsePrivateKey decodes a base64 ed25519 seed (the private key file).
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("private key must be a base64 ed25519 seed (32 bytes)")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Sign returns the detached signature published as <manifest>.sig.
func Sign(priv ed25519.PrivateKey, manifest []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)) + "\n")
}

// Verify checks a manifest's exact bytes against its .sig file.
func Verify(pub ed25519.PublicKey, manifest, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, manifest, raw) {
		return errors.New("manifest signature does not match the public key")
	}
	return nil
}

// ---- versions ----

// CompareVersions compares dotted versions numerically ("1.10" > "1.9"); a
// pre-release ("1.2.0-beta") sorts before its release.
func CompareVersions(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	a, aPre, _ := strings.Cut(strings.SplitN(a, "+", 2)[0], "-")
	b, bPre, _ := strings.Cut(strings.SplitN(b, "+", 2)[0], "-")
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(ap), len(bp)); i++ {
		var x, y int
		if i < len(ap) {
			x, _ = strconv.Atoi(ap[i])
		}
		if i < len(bp) {
			y, _ = strconv.Atoi(bp[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	case aPre < bPre:
		return -1
	}
	return 1
}

// satisfies checks a requirement like ">=0.2.0" (a bare version means >=).
func satisfies(have, req string) bool {
	req = strings.TrimSpace(req)
	if req == "" {
		return true
	}
	return CompareVersions(have, strings.TrimSpace(strings.TrimPrefix(req, ">="))) >= 0
}

// Encode renders a manifest as published (indented, trailing newline).
func (m *Manifest) Encode() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
