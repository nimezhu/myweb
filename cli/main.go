package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// Main runs the whole command line: serve (the default when the first
// argument is a flag or missing), app, version and help. It returns the exit
// code.
func Main(spec Spec) int {
	args := spec.Args
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "", "serve":
		spec.Args = args
		return Serve(spec)
	case "app", "apps":
		return appCmd(spec, args)
	case "config":
		if err := configCmd(spec, args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", spec.Name, err)
			return 1
		}
		return 0
	case "version":
		fmt.Println(spec.Name, myserver.VERSION)
		return 0
	case "help":
		usage(os.Stdout, spec.Name)
		return 0
	}
	fmt.Fprintf(os.Stderr, "%s: unknown command %q\n\n", spec.Name, cmd)
	usage(os.Stderr, spec.Name)
	return 2
}

func usage(w io.Writer, name string) {
	fmt.Fprintf(w, `usage:
  %[1]s [serve] [flags]                   serve files (%[1]s serve -h lists the flags)
  %[1]s app [list]                        list the installed apps
  %[1]s app run NAME [flags]              serve an installed app (same as -app NAME)
  %[1]s app install -from URL -key KEY    install an app, or update it, without serving it
  %[1]s app update [NAME]                 update one or all installed apps
  %[1]s app versions NAME [-all]          installed (or all published) versions; * is served
  %[1]s app use NAME VERSION|latest       serve that version and stop updating, or follow
                                          updates again; installs it if needed
  %[1]s app remove NAME [-y]              delete an installed app
  %[1]s app default [NAME | -clear]       the app plain "%[1]s" serves
  %[1]s config                            show the defaults for serve's flags
  %[1]s config set NAME VALUE             e.g. config set p 9000, config set d ~/data
  %[1]s config unset NAME
  %[1]s version
  %[1]s help

Flags given on the command line win over the config. Starting the same folder
and app again opens the copy that is running (-new starts another). Every
command takes -home DIR.
`, name)
}

// homeFor is the program's folder: dir if given, else $<NAME>_HOME or ~/.<name>.
func homeFor(name, dir string) (home.Dirs, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		return home.Dirs{Root: abs}, err
	}
	return home.For(name)
}
