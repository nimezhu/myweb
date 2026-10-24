package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nimezhu/myweb/webapp"
)

// projectFile holds a project's build settings, so a release is just
//
//	myweb-publish build -bump patch -notes "…"
const projectFile = "myweb-publish.json"

// project is myweb-publish.json; paths are relative to the file.
type project struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Out      string   `json:"out"`
	Key      string   `json:"key"`
	Channel  string   `json:"channel,omitempty"`
	Requires string   `json:"requires-myweb,omitempty"`
	Keep     int      `json:"keep,omitempty"`
	OldKeys  []string `json:"old-keys,omitempty"` // still signing during a key rotation
	Folders  []string `json:"folders,omitempty"`  // the app's folders in the data folder, e.g. "/myapp", "/samples:ro"
}

// folderFlag collects -folder PATH[:ro].
type folderFlag []webapp.Folder

func (f *folderFlag) String() string {
	var s []string
	for _, x := range *f {
		s = append(s, folderSpec(x))
	}
	return strings.Join(s, ",")
}

// folderSpec is a folder as -folder takes it.
func folderSpec(f webapp.Folder) string {
	if f.ReadOnly {
		return f.Path + ":ro"
	}
	return f.Path
}

func (f *folderFlag) Set(v string) error {
	p, ro := strings.CutSuffix(v, ":ro")
	c, err := webapp.CleanFolder(p)
	if err != nil {
		return err
	}
	if c == "/" {
		return fmt.Errorf("-folder /: an app can't ask for the whole data folder (users can grant it)")
	}
	*f = append(*f, webapp.Folder{Path: c, ReadOnly: ro})
	return nil
}

// loadProject fills in the flags that were not given from the project file.
// The default file may be missing; one named with -config may not.
func loadProject(fs *flag.FlagSet, path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && path == projectFile {
		return nil
	}
	if err != nil {
		return err
	}
	var p project
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	base := filepath.Dir(path)
	rel := func(s string) string {
		if s == "" || filepath.IsAbs(s) {
			return s
		}
		return filepath.Join(base, s)
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for flagName, v := range map[string]string{
		"name": p.Name, "dir": rel(p.Dir), "out": rel(p.Out), "key": rel(p.Key),
		"channel": p.Channel, "requires-myweb": p.Requires,
	} {
		if v != "" && !given[flagName] && fs.Lookup(flagName) != nil {
			fs.Set(flagName, v)
		}
	}
	var olds []string
	for _, k := range p.OldKeys {
		olds = append(olds, rel(k))
	}
	if len(olds) > 0 && !given["old-key"] && fs.Lookup("old-key") != nil {
		fs.Set("old-key", strings.Join(olds, ","))
	}
	if p.Keep > 0 && !given["keep"] && fs.Lookup("keep") != nil {
		fs.Set("keep", strconv.Itoa(p.Keep))
	}
	if !given["folder"] && fs.Lookup("folder") != nil {
		for _, f := range p.Folders {
			if err := fs.Set("folder", f); err != nil {
				return fmt.Errorf("%s: folders: %v", path, err)
			}
		}
	}
	return nil
}

func initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "app name, e.g. myapp")
	dir := fs.String("dir", "", "folder with the built app, e.g. dist")
	out := fs.String("out", "release", "output folder to upload")
	key := fs.String("key", "app.key", "private key file; created if missing")
	channel := fs.String("channel", "", "manifest name: <channel>.json (default stable)")
	keep := fs.Int("keep", 0, "keep only the newest N versions (0: all)")
	var folders folderFlag
	fs.Var(&folders, "folder", "a folder of the user's data folder the app uses (default /<name>); :ro to only read; repeatable")
	fs.Parse(args)
	if *name == "" || *dir == "" {
		return fmt.Errorf("usage: myweb-publish init -name NAME -dir DIR [-out release] [-key app.key]")
	}
	if !webapp.ValidName(*name) {
		return fmt.Errorf("invalid name %q: letters, digits, '.', '_' and '-'", *name)
	}
	if _, err := os.Stat(projectFile); err == nil {
		return fmt.Errorf("%s exists; edit it instead", projectFile)
	}
	p := project{Name: *name, Dir: *dir, Out: *out, Key: *key, Channel: *channel, Keep: *keep}
	for _, f := range folders {
		p.Folders = append(p.Folders, folderSpec(f))
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := writeNew(projectFile, string(b)+"\n", 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", projectFile)
	if _, err := os.Stat(*key); os.IsNotExist(err) {
		pub, err := makeKey(*key)
		if err != nil {
			return err
		}
		fmt.Printf("created %s (secret) and %s.pub (public key %s)\n", *key, *key, pub)
	} else {
		fmt.Printf("using the key in %s\n", *key)
	}
	if ignored, _ := os.ReadFile(".gitignore"); !strings.Contains(string(ignored), filepath.Base(*key)) {
		fmt.Printf("keep %s out of git, e.g.: echo %s >> .gitignore\n", *key, filepath.Base(*key))
	}
	fmt.Printf("\nfirst release:  myweb-publish build -version 1.0.0\nlater ones:     myweb-publish build -bump patch -notes \"…\"\n")
	return nil
}

// bumpVersion adds one to a part of a dotted version: 1.4.2 → 1.4.3 (patch),
// 1.5.0 (minor), 2.0.0 (major). A pre-release suffix is dropped.
func bumpVersion(v, part string) (string, error) {
	prefix := ""
	if strings.HasPrefix(v, "v") {
		prefix, v = "v", v[1:]
	}
	v = strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)[0]
	nums := []int{0, 0, 0}
	for i, s := range strings.Split(v, ".") {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return "", fmt.Errorf("cannot bump version %q", v)
		}
		nums[i] = n
	}
	switch part {
	case "patch":
		nums[2]++
	case "minor":
		nums[1], nums[2] = nums[1]+1, 0
	case "major":
		nums[0], nums[1], nums[2] = nums[0]+1, 0, 0
	default:
		return "", fmt.Errorf("-bump takes patch, minor or major, not %q", part)
	}
	return fmt.Sprintf("%s%d.%d.%d", prefix, nums[0], nums[1], nums[2]), nil
}

// versionsCmd lists what an output folder publishes.
func versionsCmd(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	config := fs.String("config", projectFile, "project settings written by init")
	out := fs.String("out", "", "output folder")
	channel := fs.String("channel", "stable", "manifest name: <channel>.json")
	fs.Parse(args)
	if err := loadProject(fs, *config); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required (or run myweb-publish init once)")
	}
	path := filepath.Join(*out, *channel+".versions.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%v (the next build writes it)", err)
	}
	var list webapp.Versions
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	fmt.Printf("%s, %d versions in %s\n", list.Name, len(list.Versions), path)
	for i, m := range list.Versions {
		mark := " "
		if i == 0 {
			mark = "*" // what <channel>.json points to
		}
		fmt.Printf("%s %-12s %s  %s\n", mark, m.Version, m.Published.Format("2006-01-02"), m.URL)
		if n := strings.TrimSpace(m.Notes); n != "" {
			fmt.Println("    " + strings.ReplaceAll(n, "\n", "\n    "))
		}
	}
	return nil
}

// rotateCmd moves the project to a new signing key in two steps. "rotate"
// makes the new key and from then on builds sign with both; myweb learns the
// new key from the first such release it installs. "rotate -finish", once
// users have updated, signs with the new key alone, and myweb stops trusting
// the old one.
func rotateCmd(args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ExitOnError)
	config := fs.String("config", projectFile, "project settings written by init")
	newKey := fs.String("new", "", "file for the new private key (default: app-2.key next to the current one)")
	finish := fs.Bool("finish", false, "stop signing with the old key")
	fs.Parse(args)
	b, err := os.ReadFile(*config)
	if err != nil {
		return fmt.Errorf("%v (rotate works on a project made with myweb-publish init)", err)
	}
	var p project
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("%s: %v", *config, err)
	}
	base := filepath.Dir(*config)
	if *finish {
		if len(p.OldKeys) == 0 {
			return fmt.Errorf("no rotation in progress")
		}
		old := p.OldKeys
		p.OldKeys = nil
		if err := saveProject(*config, p); err != nil {
			return err
		}
		fmt.Printf("builds are signed with %s only from now on; the next release makes myweb stop trusting the old key.\n", p.Key)
		fmt.Printf("users who haven't updated since the rotation started need the new public key once: %s.pub\n", p.Key)
		fmt.Printf("after that release is out, the old key can be deleted: %s\n", strings.Join(old, ", "))
		return nil
	}
	if len(p.OldKeys) > 0 {
		return fmt.Errorf("a rotation is in progress (from %s); finish it first: myweb-publish rotate -finish", strings.Join(p.OldKeys, ", "))
	}
	if *newKey == "" {
		ext := filepath.Ext(p.Key)
		stem := strings.TrimSuffix(p.Key, ext)
		if i := strings.LastIndex(stem, "-"); i > 0 {
			if _, err := strconv.Atoi(stem[i+1:]); err == nil {
				stem = stem[:i]
			}
		}
		for n := 2; ; n++ {
			*newKey = fmt.Sprintf("%s-%d%s", stem, n, ext)
			if _, err := os.Stat(filepath.Join(base, *newKey)); os.IsNotExist(err) {
				break
			}
		}
	}
	keyPath := *newKey
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(base, keyPath)
	}
	pub, err := makeKey(keyPath)
	if err != nil {
		return err
	}
	p.OldKeys, p.Key = []string{p.Key}, *newKey
	if err := saveProject(*config, p); err != nil {
		return err
	}
	fmt.Printf("created %s (secret) and %s.pub (public key %s)\n", *newKey, *newKey, pub)
	fmt.Printf("builds are now signed with %s and %s; each myweb that installs one of them trusts the new key.\n", *newKey, p.OldKeys[0])
	fmt.Printf("new users can start with -web-key %s.pub\n", *newKey)
	fmt.Printf("when users have updated (e.g. after a few releases or weeks): myweb-publish rotate -finish\n")
	return nil
}

func saveProject(path string, p project) error {
	b, _ := json.MarshalIndent(p, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
