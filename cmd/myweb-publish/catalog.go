package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nimezhu/myweb/webapp"
)

// The catalog commands keep a signed apps.json, the list of apps myweb
// installs by name (myweb app catalog, myweb app install NAME):
//
//	myweb-publish catalog init [-title TITLE]      apps.json and catalog.key, if missing
//	myweb-publish catalog add -manifest URL -app-key KEY [-name N] [-title T] [-description D]
//	                          [-icon IMG] [-screenshot IMG ...] [-tags a,b] [-homepage URL]
//	myweb-publish catalog remove NAME
//	myweb-publish catalog sign                      after editing apps.json by hand
//	myweb-publish catalog list                      check the signature, list the apps
//	myweb-publish catalog link -url URL [-name N]   the store link users add it with (URL: where
//	                                                apps.json is online)
//
// Every command takes -file apps.json and -key catalog.key (the defaults);
// the ones that change apps.json sign it again. Upload apps.json and
// apps.json.sig together.

func catalogCmd(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: myweb-publish catalog init|add|remove|sign|list|link [flags]")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("catalog "+sub, flag.ExitOnError)
	file := fs.String("file", "apps.json", "the catalog")
	keyFile := fs.String("key", "catalog.key", "the catalog's private key (catalog list reads <key>.pub)")
	switch sub {
	case "init":
		title := fs.String("title", "", "the catalog's title, e.g. \"Apps from youdata.studio\"")
		fs.Parse(args)
		return catalogInit(*file, *keyFile, *title)
	case "add":
		manifest := fs.String("manifest", "", "the app's manifest URL, e.g. https://…/stable.json")
		appKey := fs.String("app-key", "", "the app's public key, or a file holding it (e.g. app.key.pub)")
		name := fs.String("name", "", "the name to install it by (default: the manifest's)")
		title := fs.String("title", "", "a short title")
		desc := fs.String("description", "", "one or two sentences")
		homepage := fs.String("homepage", "", "a web page about the app")
		icon := fs.String("icon", "", "a square image (link; may be relative to apps.json, e.g. img/myapp.png)")
		var shots listFlag
		fs.Var(&shots, "screenshot", "an image of the app (link, may be relative); repeat for more")
		tags := fs.String("tags", "", "comma-separated, e.g. graphics,editor")
		plugins := fs.String("plugins", "", "the app's plugin store, for Home to list its profiles (a plugdex store's …/hosts/HOST/index.json)")
		noCheck := fs.Bool("no-check", false, "don't download the manifest to check its signature")
		fs.Parse(args)
		if *manifest == "" || *appKey == "" {
			return errors.New("catalog add needs -manifest URL and -app-key KEY")
		}
		return editCatalog(*file, *keyFile, func(c *webapp.Catalog) error {
			key, err := webapp.LoadPublicKey(*appKey)
			if err != nil {
				return fmt.Errorf("-app-key: %v", err)
			}
			a := webapp.CatalogApp{Name: *name, Title: *title, Description: *desc, Manifest: *manifest,
				Key: webapp.EncodePublicKey(key), Homepage: *homepage, Icon: *icon, Screenshots: shots, Plugins: *plugins}
			for _, t := range strings.Split(*tags, ",") {
				if t = strings.TrimSpace(t); t != "" {
					a.Tags = append(a.Tags, t)
				}
			}
			if !*noCheck {
				m, err := fetchManifest(*manifest, key)
				if err != nil {
					return fmt.Errorf("%v (-no-check adds it anyway)", err)
				}
				if a.Name == "" {
					a.Name = m.Name
				}
				fmt.Printf("checked %s: %s %s, signed with this key\n", *manifest, m.Name, m.Version)
			}
			for i, old := range c.Apps {
				if old.Name == a.Name {
					if a.Title == "" {
						a.Title = old.Title
					}
					if a.Description == "" {
						a.Description = old.Description
					}
					if a.Homepage == "" {
						a.Homepage = old.Homepage
					}
					if a.Icon == "" {
						a.Icon = old.Icon
					}
					if len(a.Screenshots) == 0 {
						a.Screenshots = old.Screenshots
					}
					if len(a.Tags) == 0 {
						a.Tags = old.Tags
					}
					if a.Plugins == "" {
						a.Plugins = old.Plugins
					}
					c.Apps[i] = a
					fmt.Printf("replaced %s\n", a.Name)
					return nil
				}
			}
			if a.Name == "" {
				return errors.New("give -name (the manifest was not checked)")
			}
			c.Apps = append(c.Apps, a)
			fmt.Printf("added %s\n", a.Name)
			return nil
		})
	case "remove", "rm":
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: myweb-publish catalog remove NAME")
		}
		return editCatalog(*file, *keyFile, func(c *webapp.Catalog) error {
			for i, a := range c.Apps {
				if a.Name == fs.Arg(0) {
					c.Apps = append(c.Apps[:i], c.Apps[i+1:]...)
					fmt.Printf("removed %s\n", a.Name)
					return nil
				}
			}
			return fmt.Errorf("%s is not in %s", fs.Arg(0), *file)
		})
	case "sign":
		fs.Parse(args)
		return editCatalog(*file, *keyFile, func(*webapp.Catalog) error { return nil })
	case "list", "ls":
		fs.Parse(args)
		return catalogList(*file, *keyFile+".pub")
	case "link":
		u := fs.String("url", "", "where apps.json is online, e.g. https://…/apps.json")
		name := fs.String("name", "", "what to call the store until its catalog is read (default: the catalog's title)")
		fs.Parse(args)
		return catalogLink(*file, *keyFile+".pub", *u, *name)
	}
	return fmt.Errorf("unknown command \"catalog %s\"", sub)
}

func catalogInit(file, keyFile, title string) error {
	if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
		if _, err := makeKey(keyFile); err != nil {
			return err
		}
		fmt.Printf("wrote %s (secret: keep it out of git and back it up) and %s.pub\n", keyFile, keyFile)
	}
	if _, err := os.Stat(file); err == nil {
		fmt.Printf("%s exists; signing it\n", file)
	} else if err := writeNew(file, "{}\n", 0o644); err != nil {
		return err
	}
	if err := editCatalog(file, keyFile, func(c *webapp.Catalog) error {
		if title != "" {
			c.Title = title
		}
		return nil
	}); err != nil {
		return err
	}
	pub, err := os.ReadFile(keyFile + ".pub")
	if err != nil {
		return err
	}
	fmt.Printf("\nupload %s and %s.sig, then users add the catalog with:\n  myweb app catalog add https://…/%s -key %s\n",
		file, file, filepath.Base(file), strings.TrimSpace(string(pub)))
	fmt.Printf("or its store link (myweb-publish catalog link -url https://…/%s prints it)\n", filepath.Base(file))
	return nil
}

// catalogLink prints the catalog's store link: its address and key in one,
// for users to paste into myweb (Home ▸ Stores, or myweb app catalog add).
func catalogLink(file, pubFile, u, name string) error {
	if u == "" {
		return errors.New("usage: myweb-publish catalog link -url https://…/apps.json [-name NAME]")
	}
	pub, err := os.ReadFile(pubFile)
	if err != nil {
		return err
	}
	if name == "" {
		if b, err := os.ReadFile(file); err == nil {
			var c webapp.Catalog
			if json.Unmarshal(b, &c) == nil {
				name = c.Title
			}
		}
	}
	l, err := webapp.ParseStoreLink(webapp.StoreLink{URL: u, Key: strings.TrimSpace(string(pub)), Name: name}.String())
	if err != nil {
		return err
	}
	fmt.Println(l)
	fmt.Fprintf(os.Stderr, "key fingerprint: %s (show it beside the link, so users can compare)\n", webapp.KeyFingerprint(l.Key))
	return nil
}

// editCatalog loads the catalog, changes it, checks it, writes it and signs it.
func editCatalog(file, keyFile string, change func(*webapp.Catalog) error) error {
	priv, err := loadPrivateKey(keyFile)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var c webapp.Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return fmt.Errorf("%s: %v", file, err)
	}
	if err := change(&c); err != nil {
		return err
	}
	if c.Apps == nil {
		c.Apps = []webapp.CatalogApp{}
	}
	for i := range c.Apps {
		c.Apps[i].Catalog = "" // set by myweb when it reads a catalog
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%s: %v", file, err)
	}
	data, err := c.Encode()
	if err != nil {
		return err
	}
	sig := webapp.Sign(data, priv)
	if _, err := webapp.Verify(data, sig, priv.Public().(ed25519.PublicKey)); err != nil {
		return err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(file+".sig", sig, 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s (%d apps): upload it with %s.sig\n", file, len(c.Apps), file)
	return nil
}

func catalogList(file, pubFile string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(file + ".sig")
	if err != nil {
		return err
	}
	pub, err := webapp.LoadPublicKey(pubFile)
	if err != nil {
		return err
	}
	if _, err := webapp.Verify(data, sig, pub); err != nil {
		return fmt.Errorf("%s: %v (run myweb-publish catalog sign)", file, err)
	}
	var c webapp.Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	fmt.Printf("%s: signature ok, %d apps\n", file, len(c.Apps))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, a := range c.Apps {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", a.Name, a.Title, a.Manifest)
	}
	return tw.Flush()
}

// fetchManifest downloads a manifest and checks it is signed with key.
func fetchManifest(u string, key ed25519.PublicKey) (*webapp.Manifest, error) {
	get := func(u string) ([]byte, error) {
		c := http.Client{Timeout: 20 * time.Second}
		resp, err := c.Get(u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	data, err := get(u)
	if err != nil {
		return nil, err
	}
	sig, err := get(u + ".sig")
	if err != nil {
		return nil, err
	}
	if _, err := webapp.Verify(data, sig, key); err != nil {
		return nil, fmt.Errorf("%s: %v", u, err)
	}
	var m webapp.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// listFlag collects a repeated flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }
