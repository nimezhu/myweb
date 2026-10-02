// myweb-publish prepares web app releases that myweb installs with -web-from.
//
//	myweb-publish keygen -out app.key
//	    writes a private key (keep it secret, e.g. in CI secrets) and prints
//	    the public key to give to myweb with -web-key
//	myweb-publish build -key app.key -name cellome -version 1.4.2 -dir ./dist -out ./release
//	    zips ./dist into release/cellome-1.4.2.zip and writes the signed
//	    manifest release/stable.json + stable.json.sig; upload release/ as is
//	myweb-publish verify -pub <public key> -manifest release/stable.json
//	    checks the signature and the bundle before uploading
package main

import (
	"archive/zip"
	"bytes"
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
	case "build":
		err = build(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "myweb-publish:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  myweb-publish keygen -out app.key
  myweb-publish build  -key app.key -name NAME -version V -dir DIR -out OUTDIR [-channel stable] [-requires-myweb 0.1.0] [-notes TEXT]
  myweb-publish verify -pub PUBLIC_KEY -manifest OUTDIR/stable.json
`)
	os.Exit(2)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "file for the private key (must not exist)")
	fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	pub, priv, err := webapp.GenerateKey()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%v (keygen never overwrites a key)", err)
	}
	fmt.Fprintln(f, priv)
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("private key: %s (keep it secret)\npublic key:  %s\n\nstart myweb with:  -web-from <URL>/stable.json -web-key %s\n", *out, pub, pub)
	return nil
}

func build(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	keyFile := fs.String("key", "", "private key file from keygen")
	name := fs.String("name", "", "app name")
	version := fs.String("version", "", "version, e.g. 1.4.2")
	dir := fs.String("dir", "", "folder with the built app (index.html at its top)")
	out := fs.String("out", "", "output folder to upload")
	channel := fs.String("channel", "stable", "manifest name: <channel>.json")
	requires := fs.String("requires-myweb", "", "lowest myweb version the app needs, e.g. 0.1.0")
	notes := fs.String("notes", "", "release notes")
	fs.Parse(args)
	if *keyFile == "" || *name == "" || *version == "" || *dir == "" || *out == "" {
		return fmt.Errorf("-key, -name, -version, -dir and -out are required")
	}
	keyText, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	priv, err := webapp.ParsePrivateKey(string(keyText))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(*dir, "index.html")); err != nil {
		return fmt.Errorf("%s has no index.html at its top", *dir)
	}
	manifestPath := filepath.Join(*out, *channel+".json")
	if old, err := os.ReadFile(manifestPath); err == nil {
		var prev webapp.Manifest
		if json.Unmarshal(old, &prev) == nil && webapp.CompareVersions(*version, prev.Version) <= 0 {
			return fmt.Errorf("version %s is not newer than the published %s (myweb never installs an older version)", *version, prev.Version)
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
	raw, err := m.Encode()
	if err != nil {
		return err
	}
	// write the signature first: a manifest is never published unsigned
	if err := os.WriteFile(manifestPath+".sig", webapp.Sign(priv, raw), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s %s: %s (%d bytes)\n  %s\n  %s.sig\nupload the contents of %s\n", *name, *version, zipName, len(bundle), manifestPath, manifestPath, *out)
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
	pubText := fs.String("pub", "", "public key")
	manifestPath := fs.String("manifest", "", "manifest file, e.g. release/stable.json")
	fs.Parse(args)
	pub, err := webapp.ParsePublicKey(*pubText)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*manifestPath + ".sig")
	if err != nil {
		return err
	}
	if err := webapp.Verify(pub, raw, sig); err != nil {
		return err
	}
	var m webapp.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if !strings.Contains(m.URL, "://") {
		bundle, err := os.ReadFile(filepath.Join(filepath.Dir(*manifestPath), m.URL))
		if err != nil {
			return err
		}
		if sum := sha256.Sum256(bundle); hex.EncodeToString(sum[:]) != m.SHA256 {
			return fmt.Errorf("%s does not match the manifest's sha256", m.URL)
		}
	}
	fmt.Printf("ok: %s %s, signature and bundle verified\n", m.Name, m.Version)
	return nil
}
