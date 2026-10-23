// myweb-publish prepares web app releases that myweb installs with -web-from.
//
//	myweb-publish init -name myapp -dir dist
//	    writes myweb-publish.json (and app.key + app.key.pub if missing), so
//	    that releases need no more than: myweb-publish build -bump patch
//	myweb-publish keygen -out app.key
//	    writes a private key to app.key (keep it secret, e.g. in CI secrets)
//	    and its public key to app.key.pub, to give to myweb with -web-key
//	myweb-publish pubkey -key app.key
//	    prints the public key of a private key (if app.key.pub was lost)
//	myweb-publish build -key app.key -name cellome -version 1.4.2 -dir ./dist -out ./release
//	    zips ./dist into release/cellome-1.4.2.zip and writes the signed
//	    manifest release/stable.json and the signed list of every version,
//	    release/stable.versions.json; upload release/ as is
//	myweb-publish versions
//	    lists what the output folder publishes
//	myweb-publish verify -pub app.key.pub -manifest release/stable.json
//	    checks the signature and the bundle before uploading
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nimezhu/myweb/webapp"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "pubkey":
		err = pubkey(os.Args[2:])
	case "init":
		err = initCmd(os.Args[2:])
	case "build":
		err = build(os.Args[2:])
	case "versions":
		err = versionsCmd(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "-h", "-help", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "myweb-publish: unknown command %q\n", os.Args[1])
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "myweb-publish:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  myweb-publish init -name NAME -dir DIR [-out release] [-key app.key]
                                           writes myweb-publish.json, and the key if missing
  myweb-publish build -version V | -bump patch|minor|major [-notes TEXT] [-keep N]
                                           with myweb-publish.json; without it also give
                                           -key app.key -name NAME -dir DIR -out OUTDIR
                                           [-channel stable] [-requires-myweb 0.1.0]
  myweb-publish versions                   what the output folder publishes
  myweb-publish verify -pub app.key.pub|KEY -manifest OUTDIR/stable.json   (or -key app.key)
  myweb-publish keygen -out app.key        writes app.key (secret) and app.key.pub
  myweb-publish pubkey -key app.key        prints the public key of app.key
`)
	os.Exit(2)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "file for the private key (must not exist); the public key goes to <out>.pub")
	fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	pub, err := makeKey(*out)
	if err != nil {
		return err
	}
	fmt.Printf("private key: %s (keep it secret)\npublic key:  %s  (also in %s.pub)\n\nstart myweb with:  -web-from <URL>/stable.json -web-key %s\n", *out, pub, *out, pub)
	return nil
}

// makeKey writes a new private key to path and its public key to path.pub,
// never overwriting either.
func makeKey(path string) (pub string, err error) {
	pubFile := path + ".pub"
	for _, p := range []string{path, pubFile} {
		if _, err := os.Lstat(p); err == nil {
			return "", fmt.Errorf("%s exists (a key is never overwritten)", p)
		}
	}
	pub, priv, err := webapp.GenerateKey()
	if err != nil {
		return "", err
	}
	if err := writeNew(path, priv+"\n", 0o600); err != nil {
		return "", err
	}
	if err := writeNew(pubFile, pub+"\n", 0o644); err != nil {
		os.Remove(path) // a private key without its public key is no use
		return "", err
	}
	return pub, nil
}

// writeNew writes a file that must not exist yet.
func writeNew(path, content string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func pubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	keyFile := fs.String("key", "", "private key file from keygen")
	fs.Parse(args)
	if *keyFile == "" {
		return fmt.Errorf("-key is required")
	}
	priv, err := loadPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	fmt.Println(webapp.EncodePublicKey(priv.Public().(ed25519.PublicKey)))
	return nil
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	// public keys and private seeds are both 32 bytes, so only the name tells them apart
	if strings.HasSuffix(path, ".pub") {
		return nil, fmt.Errorf("%s is a public key; -key needs the private key file", path)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	priv, err := webapp.ParsePrivateKey(string(text))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return priv, nil
}

func build(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	config := fs.String("config", projectFile, "project settings written by init; flags given here win")
	keyFile := fs.String("key", "", "private key file from keygen")
	name := fs.String("name", "", "app name")
	version := fs.String("version", "", "version, e.g. 1.4.2")
	bump := fs.String("bump", "", "or: the published version plus one: patch, minor or major")
	dir := fs.String("dir", "", "folder with the built app (index.html at its top)")
	out := fs.String("out", "", "output folder to upload")
	channel := fs.String("channel", "stable", "manifest name: <channel>.json")
	requires := fs.String("requires-myweb", "", "lowest myweb version the app needs, e.g. 0.1.0")
	notes := fs.String("notes", "", "release notes")
	keep := fs.Int("keep", 0, "keep only the newest N versions in the folder and the version list (0: all)")
	fs.Parse(args)
	if err := loadProject(fs, *config); err != nil {
		return err
	}
	if *keyFile == "" || *name == "" || *dir == "" || *out == "" {
		return fmt.Errorf("-key, -name, -dir and -out are required (or run myweb-publish init once)")
	}
	if (*version == "") == (*bump == "") {
		return fmt.Errorf("give -version V or -bump patch|minor|major")
	}
	priv, err := loadPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	// a <key>.pub that doesn't belong to this key would make every install fail
	if text, err := os.ReadFile(*keyFile + ".pub"); err == nil {
		if have, err := webapp.ParsePublicKey(string(text)); err != nil || !have.Equal(pub) {
			return fmt.Errorf("%s.pub is not the public key of %s (myweb-publish pubkey -key %s prints the right one)", *keyFile, *keyFile, *keyFile)
		}
	}
	if _, err := os.Stat(filepath.Join(*dir, "index.html")); err != nil {
		return fmt.Errorf("%s has no index.html at its top", *dir)
	}
	manifestPath := filepath.Join(*out, *channel+".json")
	listPath := filepath.Join(*out, *channel+".versions.json")
	var prev *webapp.Manifest
	if old, err := os.ReadFile(manifestPath); err == nil {
		prev = &webapp.Manifest{}
		if err := json.Unmarshal(old, prev); err != nil {
			return fmt.Errorf("%s: %v", manifestPath, err)
		}
		if prev.Name != *name {
			return fmt.Errorf("%s is for %q, not %q", manifestPath, prev.Name, *name)
		}
	}
	if *bump != "" {
		if prev == nil {
			return fmt.Errorf("nothing published in %s yet: give the first version with -version", *out)
		}
		if *version, err = bumpVersion(prev.Version, *bump); err != nil {
			return err
		}
	}
	if prev != nil && webapp.CompareVersions(*version, prev.Version) <= 0 {
		return fmt.Errorf("version %s is not newer than the published %s (myweb never installs an older version by itself)", *version, prev.Version)
	}

	// the list of every published version, so myweb can install any of them;
	// a folder published before the list existed starts it with its latest
	var list webapp.Versions
	if old, err := os.ReadFile(listPath); err == nil {
		if err := json.Unmarshal(old, &list); err != nil {
			return fmt.Errorf("%s: %v", listPath, err)
		}
	} else if prev != nil {
		if _, err := os.Stat(filepath.Join(*out, filepath.FromSlash(prev.URL))); err == nil || strings.Contains(prev.URL, "://") {
			list.Versions = []webapp.Manifest{*prev}
		}
	}

	bundle, err := zipDir(*dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	zipName := *name + "-" + *version + ".zip"
	if err := os.WriteFile(filepath.Join(*out, zipName), bundle, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(bundle)
	m := webapp.Manifest{
		Name: *name, Version: *version, URL: zipName,
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bundle)),
		Requires:  webapp.Requires{Myweb: *requires},
		Published: time.Now().UTC().Truncate(time.Second), Notes: *notes,
	}
	if err := m.Validate(); err != nil {
		return err
	}
	list.Name = *name
	list.Versions = append(list.Versions, m)
	list.Sort()
	var dropped []webapp.Manifest
	if *keep > 0 && len(list.Versions) > *keep {
		list.Versions, dropped = list.Versions[:*keep], list.Versions[*keep:]
	}

	raw, err := m.Encode()
	if err != nil {
		return err
	}
	listRaw, err := list.Encode()
	if err != nil {
		return err
	}
	// signatures first: a manifest or list is never published unsigned
	for _, f := range []struct {
		path string
		raw  []byte
	}{{listPath, listRaw}, {manifestPath, raw}} {
		if err := os.WriteFile(f.path+".sig", webapp.Sign(priv, f.raw), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, f.raw, 0o644); err != nil {
			return err
		}
	}
	for _, d := range dropped {
		if !strings.Contains(d.URL, "://") {
			os.Remove(filepath.Join(*out, filepath.FromSlash(d.URL)))
		}
	}
	if _, err := readRelease(pub, manifestPath); err != nil {
		return fmt.Errorf("the written release does not verify: %v", err)
	}
	fmt.Printf("%s %s: %s (%d bytes), %d listed", *name, *version, zipName, len(bundle), len(list.Versions))
	if len(dropped) > 0 {
		fmt.Printf(", %d older removed", len(dropped))
	}
	fmt.Printf("\nsigned for public key %s\nupload the contents of %s\n", webapp.EncodePublicKey(pub), *out)
	return nil
}

// zipDir zips a folder in a stable order, leaving out dotfiles (.git, .DS_Store, …).
func zipDir(dir string) ([]byte, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		w, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()})
		if err != nil {
			return nil, err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pubText := fs.String("pub", "", "public key, or a file holding it (e.g. app.key.pub)")
	keyFile := fs.String("key", "", "or: the private key file, to check against its public key")
	manifestPath := fs.String("manifest", "", "manifest file, e.g. release/stable.json")
	fs.Parse(args)
	if *manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	var pub ed25519.PublicKey
	switch {
	case *pubText != "" && *keyFile != "":
		return fmt.Errorf("give -pub or -key, not both")
	case *pubText != "":
		var err error
		if pub, err = webapp.LoadPublicKey(*pubText); err != nil {
			return err
		}
	case *keyFile != "":
		priv, err := loadPrivateKey(*keyFile)
		if err != nil {
			return err
		}
		pub = priv.Public().(ed25519.PublicKey)
	default:
		return fmt.Errorf("-pub (or -key) is required")
	}
	m, err := readRelease(pub, *manifestPath)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %s %s, signatures and bundles verified\n", m.Name, m.Version)
	return nil
}

// readRelease checks a manifest as myweb would: its signature, its fields and,
// when the bundle sits next to it, the bundle's size and hash.
func readRelease(pub ed25519.PublicKey, manifestPath string) (*webapp.Manifest, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(manifestPath + ".sig")
	if err != nil {
		return nil, err
	}
	if err := webapp.Verify(pub, raw, sig); err != nil {
		return nil, fmt.Errorf("%v (public key %s); was it signed with another key, or edited after signing?", err, webapp.EncodePublicKey(pub))
	}
	var m webapp.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest: %v", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	dir := filepath.Dir(manifestPath)
	if err := checkBundle(dir, &m); err != nil {
		return nil, err
	}
	// the version list, when there is one: signed too, and every zip it names
	listPath := strings.TrimSuffix(manifestPath, ".json") + ".versions.json"
	listRaw, err := os.ReadFile(listPath)
	if os.IsNotExist(err) {
		return &m, nil
	}
	if err != nil {
		return nil, err
	}
	listSig, err := os.ReadFile(listPath + ".sig")
	if err != nil {
		return nil, err
	}
	if err := webapp.Verify(pub, listRaw, listSig); err != nil {
		return nil, fmt.Errorf("%s: %v", listPath, err)
	}
	var list webapp.Versions
	if err := json.Unmarshal(listRaw, &list); err != nil {
		return nil, fmt.Errorf("%s: %v", listPath, err)
	}
	if list.Name != m.Name {
		return nil, fmt.Errorf("%s is for %q, the manifest for %q", listPath, list.Name, m.Name)
	}
	if _, ok := list.Find(m.Version); !ok {
		return nil, fmt.Errorf("%s does not list the published %s", listPath, m.Version)
	}
	for i := range list.Versions {
		if err := list.Versions[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s: %v", listPath, err)
		}
		if err := checkBundle(dir, &list.Versions[i]); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// checkBundle compares a zip next to the manifest with its size and hash.
func checkBundle(dir string, m *webapp.Manifest) error {
	if strings.Contains(m.URL, "://") {
		return nil
	}
	bundle, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(m.URL)))
	if err != nil {
		return err
	}
	if int64(len(bundle)) != m.Size {
		return fmt.Errorf("%s is %d bytes, the manifest says %d", m.URL, len(bundle), m.Size)
	}
	if sum := sha256.Sum256(bundle); hex.EncodeToString(sum[:]) != m.SHA256 {
		return fmt.Errorf("%s does not match the manifest's sha256", m.URL)
	}
	return nil
}
