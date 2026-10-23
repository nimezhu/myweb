package webapp

import (
	"errors"
	"flag"
	"fmt"
	"time"
)

// Flags are the command-line options for installing the web app from a
// distribution server; apps register them with AddFlags.
type Flags struct {
	App      string
	List     bool
	From     string
	Key      string
	Version  string
	NoUpdate bool
	Check    time.Duration
}

// AddFlags registers -app, -apps, -web-from, -web-key, -web-version, -no-update and -web-check.
func AddFlags(fs *flag.FlagSet) *Flags {
	f := &Flags{}
	fs.StringVar(&f.App, "app", "", "serve this installed app at / and keep it updated (instead of -web-from and -web-key)")
	fs.BoolVar(&f.List, "apps", false, "list the installed apps and exit")
	fs.StringVar(&f.From, "web-from", "", "install the web app at / from this signed manifest URL and keep it updated")
	fs.StringVar(&f.Key, "web-key", "", "public key (or a file holding it, e.g. app.key.pub) that signs the -web-from manifest")
	fs.StringVar(&f.Version, "web-version", "", "with -web-from: serve this installed version and don't update (rollback)")
	fs.BoolVar(&f.NoUpdate, "no-update", false, "with -web-from: serve the installed version, don't check for updates")
	fs.DurationVar(&f.Check, "web-check", defaultInterval, "with -web-from: how often to check for updates")
	return f
}

// Installer builds the installer for these flags, or returns nil when
// -web-from is not set. Apps are installed under appsDir (e.g. ~/.myweb/apps).
func (f *Flags) Installer(mywebVersion, appsDir string, onUpdate func(name, version string)) (*Installer, error) {
	if f.App != "" {
		if f.From != "" || f.Key != "" {
			return nil, errors.New("-app replaces -web-from and -web-key; give one or the other")
		}
		a, err := FindApp(appsDir, f.App)
		if err != nil {
			return nil, err
		}
		if a.Key == "" {
			return nil, fmt.Errorf("%s was installed before myweb saved its key; start it once more with\n  -web-from %s -web-key <public key>", a.Name, a.Manifest)
		}
		f.From, f.Key = a.Manifest, a.Key
	}
	if f.From == "" {
		if f.Key != "" || f.Version != "" {
			return nil, errors.New("-web-key and -web-version need -web-from")
		}
		return nil, nil
	}
	if f.Key == "" {
		return nil, errors.New("-web-from needs -web-key: the app is only installed if its signature matches")
	}
	pub, err := LoadPublicKey(f.Key)
	if err != nil {
		return nil, err
	}
	return New(Options{
		ManifestURL: f.From, PublicKey: pub, Pin: f.Version, NoUpdate: f.NoUpdate, Dir: appsDir,
		Interval: f.Check, Myweb: mywebVersion, OnUpdate: onUpdate,
	})
}
