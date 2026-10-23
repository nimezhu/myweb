package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nimezhu/myweb/home"
)

func TestConfigApply(t *testing.T) {
	dirs := home.Dirs{Root: t.TempDir()}
	if err := os.WriteFile(configFile(dirs), []byte(`{"p": 9000, "no-open": true, "app": "hello", "d": "~/data"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(dirs)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := os.UserHomeDir()

	// the config fills in what the command line leaves out
	fs, f := newServeFlags(Spec{Name: "myweb"})
	fs.Parse([]string{"-p", "7000"})
	if err := c.apply(fs, "config.json"); err != nil {
		t.Fatal(err)
	}
	if *f.port != 7000 || !*f.noOpen || f.webFlags.App != "hello" || *f.directory != filepath.Join(h, "data") {
		t.Fatalf("p %d no-open %v app %q d %q", *f.port, *f.noOpen, f.webFlags.App, *f.directory)
	}

	// any app source on the command line replaces the config's app
	for _, args := range [][]string{{"-web", "dist"}, {"-web-from", "https://x/stable.json"}, {"-app", ""}} {
		fs, f := newServeFlags(Spec{Name: "myweb"})
		fs.Parse(args)
		if err := c.apply(fs, "config.json"); err != nil {
			t.Fatal(err)
		}
		if f.webFlags.App != "" {
			t.Errorf("%v: config app %q still applied", args, f.webFlags.App)
		}
	}

	for _, bad := range []config{{"nope": "1"}, {"home": "/x"}, {"p": "abc"}} {
		fs, _ := newServeFlags(Spec{Name: "myweb"})
		fs.Parse(nil)
		if err := bad.apply(fs, "config.json"); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
