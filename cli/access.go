package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// An installed app may use only its folders: those its publisher declares,
// and those granted with
//
//	app allow NAME FOLDER [-write]    let it read (-write: and change) a folder
//	app deny NAME FOLDER              take a grant back
//	app access NAME                   what it may use
//
// "/" grants the whole data folder.

// limitApp is the server's limit for the app; its declared folders are
// created in the data folder root.
func limitApp(app *webapp.Installer, root string) *myserver.AppAccess {
	makeFolders(app, root)
	return &myserver.AppAccess{
		Name: app.Status().Name,
		Folders: func() []myserver.Folder {
			var out []myserver.Folder
			for _, f := range app.Access() {
				out = append(out, myserver.Folder{Path: f.Path, ReadOnly: f.ReadOnly})
			}
			return out
		},
	}
}

// makeFolders creates the folders the app declares, so it finds them.
func makeFolders(app *webapp.Installer, root string) {
	for _, f := range app.Declared() {
		if !f.ReadOnly {
			os.MkdirAll(filepath.Join(root, filepath.FromSlash(f.Path)), 0o755)
		}
	}
}

func folderList(fs []webapp.Folder) string {
	if len(fs) == 0 {
		return "nothing"
	}
	var s []string
	for _, f := range fs {
		s = append(s, f.String())
	}
	return strings.Join(s, ", ")
}

func appAllow(name string, args []string) error {
	f := newAppFlags(name, "app allow")
	write := f.Bool("write", false, "")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{fmt.Errorf("usage: %s app allow NAME FOLDER [-write]", name)}
	}
	return changeAccess(name, *f.home, pos[0], func(in *webapp.Installer) error {
		if err := in.Grant(webapp.Folder{Path: pos[1], ReadOnly: !*write}); err != nil {
			return err
		}
		if p, _ := webapp.CleanFolder(pos[1]); p == "/" {
			fmt.Printf("%s may now use the whole data folder\n", pos[0])
		}
		return nil
	})
}

func appDeny(name string, args []string) error {
	f := newAppFlags(name, "app deny")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{fmt.Errorf("usage: %s app deny NAME FOLDER", name)}
	}
	return changeAccess(name, *f.home, pos[0], func(in *webapp.Installer) error { return in.Revoke(pos[1]) })
}

func appAccess(name string, args []string) error {
	f := newAppFlags(name, "app access")
	a, err := f.oneApp(args)
	if err != nil {
		return err
	}
	printAccess(a)
	return nil
}

func printAccess(a webapp.App) {
	granted := map[string]bool{}
	for _, g := range a.Grants {
		granted[g.Path] = true
	}
	fmt.Printf("%s may use (in the data folder):\n", a.Name)
	for _, f := range a.Access {
		mode := "read and write"
		if f.ReadOnly {
			mode = "read only"
		}
		from := "its own"
		if granted[f.Path] {
			from = "granted"
		}
		fmt.Printf("  %-28s %-15s %s\n", f.Path, mode, from)
	}
}

// changeAccess changes an installed app's grants, shows the result and tells
// running servers.
func changeAccess(name, homeDir, app string, change func(*webapp.Installer) error) error {
	dirs, err := homeFor(name, homeDir)
	if err != nil {
		return err
	}
	a, err := webapp.FindApp(dirs.Apps(), app)
	if err != nil {
		return err
	}
	if a.Key == "" {
		return errors.New(a.Name + " was installed before keys were saved; install it again first")
	}
	in, err := (&webapp.Flags{From: a.Manifest, Key: a.Key}).Installer(myserver.VERSION, dirs.Apps(), nil)
	if err != nil {
		return err
	}
	if err := change(in); err != nil {
		return err
	}
	if a, err = webapp.FindApp(dirs.Apps(), app); err != nil {
		return err
	}
	printAccess(a)
	if n := tellRunning(dirs, a.Manifest); n > 0 {
		fmt.Printf("(the running %s uses this now)\n", a.Name)
	}
	return nil
}
