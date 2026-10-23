package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// appCmd runs "app …": managing the web apps installed with -web-from.
func appCmd(spec Spec, args []string) int {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "run" {
		return appRun(spec, args)
	}
	cmds := map[string]func(name string, args []string) error{
		"list": appList, "ls": appList,
		"install":  appInstall,
		"update":   appUpdate,
		"versions": appVersions,
		"remove":   appRemove, "rm": appRemove,
	}
	if sub == "help" {
		usage(os.Stdout, spec.Name)
		return 0
	}
	f, ok := cmds[sub]
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown command \"app %s\"\n\n", spec.Name, sub)
		usage(os.Stderr, spec.Name)
		return 2
	}
	if err := f(spec.Name, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", spec.Name, err)
		return 1
	}
	return 0
}

// appFlags is a flag set with -home; parse accepts flags before and after
// the names.
type appFlags struct {
	*flag.FlagSet
	name string
	home *string
}

func newAppFlags(name, sub string) appFlags {
	fs := flag.NewFlagSet(name+" app "+sub, flag.ContinueOnError)
	return appFlags{fs, name, fs.String("home", "", fmt.Sprintf("%s's own folder (default $%s, or ~/.%s)", name, home.EnvVar(name), name))}
}

func (f appFlags) parse(args []string) ([]string, error) {
	var names []string
	for {
		if err := f.Parse(args); err != nil {
			return nil, err
		}
		if args = f.Args(); len(args) == 0 {
			return names, nil
		}
		names, args = append(names, args[0]), args[1:]
	}
}

// oneApp parses the flags and finds the one app they name.
func (f appFlags) oneApp(args []string) (webapp.App, error) {
	names, err := f.parse(args)
	if err != nil {
		return webapp.App{}, err
	}
	if len(names) != 1 {
		return webapp.App{}, fmt.Errorf("usage: %s NAME", f.Name())
	}
	dirs, err := homeFor(f.name, *f.home)
	if err != nil {
		return webapp.App{}, err
	}
	return webapp.FindApp(dirs.Apps(), names[0])
}

func appList(name string, args []string) error {
	f := newAppFlags(name, "list")
	if names, err := f.parse(args); err != nil {
		return err
	} else if len(names) > 0 {
		return fmt.Errorf("usage: %s app list", name)
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	return printApps(name, dirs.Apps())
}

func printApps(name, dir string) error {
	apps, err := webapp.InstalledApps(dir)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		fmt.Printf("no apps installed in %s; install one with: %s app install -from URL -key KEY\n", dir, name)
		return nil
	}
	fmt.Println(webapp.FormatApps(apps))
	return nil
}

// appRun is "app run NAME [serve flags]", i.e. serve -app NAME.
func appRun(spec Spec, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(os.Stderr, "usage: %s app run NAME [flags]\n", spec.Name)
		return 2
	}
	spec.Args = append([]string{"-app", args[0]}, args[1:]...)
	return Serve(spec)
}

func appInstall(name string, args []string) error {
	f := newAppFlags(name, "install")
	from := f.String("from", "", "the app's signed manifest URL, e.g. https://…/stable.json")
	key := f.String("key", "", "its public key, or a file holding it")
	if names, err := f.parse(args); err != nil {
		return err
	} else if len(names) > 0 || *from == "" || *key == "" {
		return fmt.Errorf("usage: %s app install -from URL -key KEY", name)
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	return update(name, dirs.Apps(), &webapp.Flags{From: *from, Key: *key})
}

func appUpdate(name string, args []string) error {
	f := newAppFlags(name, "update")
	names, err := f.parse(args)
	if err != nil {
		return err
	}
	dirs, err := homeFor(name, *f.home)
	if err != nil {
		return err
	}
	var apps []webapp.App
	if len(names) == 0 {
		if apps, err = webapp.InstalledApps(dirs.Apps()); err != nil {
			return err
		}
		if len(apps) == 0 {
			return printApps(name, dirs.Apps())
		}
	}
	for _, n := range names {
		a, err := webapp.FindApp(dirs.Apps(), n)
		if err != nil {
			return err
		}
		apps = append(apps, a)
	}
	failed := 0
	for _, a := range apps {
		if a.Key == "" {
			fmt.Fprintf(os.Stderr, "%s: installed before its key was saved; run once:\n  %s app install -from %s -key KEY\n", a.Name, name, a.Manifest)
			failed++
			continue
		}
		if err := update(name, dirs.Apps(), &webapp.Flags{From: a.Manifest, Key: a.Key}); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", a.Name, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d apps not updated", failed, len(apps))
	}
	return nil
}

// update checks one app's manifest now and installs a newer version.
func update(name, appsDir string, fl *webapp.Flags) error {
	in, err := fl.Installer(myserver.VERSION, appsDir, nil)
	if err != nil {
		return err
	}
	before := in.Current()
	log.SetOutput(io.Discard) // the installer logs for the server; here the result is printed
	err = in.Update(context.Background())
	log.SetOutput(os.Stderr)
	if err != nil {
		return err
	}
	a, err := webapp.FindApp(appsDir, fl.From)
	if err != nil {
		return err
	}
	switch {
	case before == "":
		fmt.Printf("%s %s installed; serve it with: %s app run %s\n", a.Name, a.Current, name, a.Name)
	case before != a.Current:
		fmt.Printf("%s %s → %s (a running %s switches at its next check, or press a)\n", a.Name, before, a.Current, name)
	default:
		fmt.Printf("%s %s is up to date\n", a.Name, a.Current)
	}
	return nil
}

func appVersions(name string, args []string) error {
	a, err := newAppFlags(name, "versions").oneApp(args)
	if err != nil {
		return err
	}
	fmt.Printf("%s from %s\n", a.Name, a.Manifest)
	for _, v := range a.Versions {
		mark := " "
		if v == a.Current {
			mark = "*"
		}
		fmt.Printf("%s %s\n", mark, v)
	}
	return nil
}

func appRemove(name string, args []string) error {
	f := newAppFlags(name, "remove")
	yes := f.Bool("y", false, "don't ask")
	a, err := f.oneApp(args)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Printf("remove %s (installed: %s) from %s? [y/N] ", a.Name, strings.Join(a.Versions, ", "), a.Dir)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return errors.New("not removed")
		}
	}
	if err := os.RemoveAll(a.Dir); err != nil {
		return fmt.Errorf("%v (is a %s still serving it?)", err, name)
	}
	fmt.Printf("%s removed\n", a.Name)
	return nil
}
