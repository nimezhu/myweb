package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/internal/fsx"
)

// config is <home>/config.json: default values for serve's flags, by flag
// name, e.g. {"p": "9000", "d": "~/data", "app": "myapp"}. Flags given on the
// command line win.
type config map[string]string

// notInConfig are flags that make no sense as a default.
var notInConfig = map[string]bool{"home": true, "v": true, "version": true, "apps": true, "new": true}

// appSource flags pick what is served at "/"; one given on the command line
// replaces all of them from the config.
var appSource = []string{"app", "web", "web-from", "web-key"}

func configFile(dirs home.Dirs) string { return filepath.Join(dirs.Root, "config.json") }

func loadConfig(dirs home.Dirs) (config, error) {
	b, err := os.ReadFile(configFile(dirs))
	if os.IsNotExist(err) {
		return config{}, nil
	}
	if err != nil {
		return nil, err
	}
	// hand-edited files may use JSON numbers and booleans
	var raw map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %v", configFile(dirs), err)
	}
	c := config{}
	for k, v := range raw {
		if _, ok := v.(map[string]interface{}); ok {
			return nil, fmt.Errorf("%s: %q must be a plain value", configFile(dirs), k)
		}
		c[k] = fmt.Sprint(v)
	}
	return c, nil
}

func (c config) save(dirs home.Dirs) error {
	if err := os.MkdirAll(dirs.Root, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := configFile(dirs) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return fsx.Rename(tmp, configFile(dirs))
}

// apply sets the flags that the command line left out.
func (c config) apply(fs *flag.FlagSet, file string) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, n := range appSource {
		if given[n] {
			for _, m := range appSource {
				given[m] = true
			}
			break
		}
	}
	for _, k := range c.keys() {
		if given[k] {
			continue
		}
		if err := checkKey(fs, k); err != nil {
			return fmt.Errorf("%s: %v", file, err)
		}
		if err := fs.Set(k, expandHome(c[k])); err != nil {
			return fmt.Errorf("%s: %s: %v", file, k, err)
		}
	}
	return nil
}

func checkKey(fs *flag.FlagSet, k string) error {
	if notInConfig[k] {
		return fmt.Errorf("%q can't be set in the config", k)
	}
	if fs.Lookup(k) == nil {
		return fmt.Errorf("unknown flag %q", k)
	}
	return nil
}

func (c config) keys() []string {
	var ks []string
	for k := range c {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// expandHome turns a leading ~/ into the home folder, so paths in the config
// work as they would in a shell.
func expandHome(v string) string {
	if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, v[1:])
		}
	}
	return v
}

// configCmd is "config [set NAME VALUE | unset NAME]".
func configCmd(spec Spec, args []string) error {
	f := newAppFlags(spec.Name, "config")
	if len(args) > 0 && args[0] == "help" {
		return flag.ErrHelp
	}
	words, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(spec.Name, *f.home)
	if err != nil {
		return err
	}
	c, err := loadConfig(dirs)
	if err != nil {
		return err
	}
	switch {
	case len(words) == 0:
		fmt.Printf("%s\n", configFile(dirs))
		if len(c) == 0 {
			fmt.Printf("  (empty; e.g. %s config set p 9000)\n", spec.Name)
		}
		for _, k := range c.keys() {
			fmt.Printf("  %-12s %s\n", k, c[k])
		}
		return nil
	case words[0] == "set" && len(words) == 3:
		k, v := strings.TrimPrefix(words[1], "-"), words[2]
		fs, _ := newServeFlags(spec)
		if err := checkKey(fs, k); err != nil {
			return err
		}
		if err := fs.Set(k, expandHome(v)); err != nil {
			return fmt.Errorf("%s: %v", k, err)
		}
		c[k] = v
	case words[0] == "unset" && len(words) == 2:
		k := strings.TrimPrefix(words[1], "-")
		if _, ok := c[k]; !ok {
			return fmt.Errorf("%s is not set", k)
		}
		delete(c, k)
	default:
		return usageError{errors.New("config takes no arguments, set NAME VALUE or unset NAME")}
	}
	if err := c.save(dirs); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", configFile(dirs))
	return nil
}
