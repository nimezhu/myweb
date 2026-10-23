package cli

import (
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
  %[1]s app versions NAME                 installed versions; * marks the one served
  %[1]s app remove NAME [-y]              delete an installed app
  %[1]s version
  %[1]s help

The app commands take -home DIR, like serve.
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
