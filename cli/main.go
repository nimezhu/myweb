package cli

import (
	"errors"
	"flag"
	"fmt"
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
	case "completion":
		return finish(spec, "completion", completionCmd(spec, args, os.Stdout))
	case "__complete": // used by the completion scripts
		for _, c := range completions(spec, args) {
			fmt.Println(c)
		}
		return 0
	case "service":
		return finish(spec, "service", serviceCmd(spec, args))
	case "config":
		return finish(spec, "config", configCmd(spec, args))
	case "version":
		fmt.Println(spec.Name, myserver.VERSION)
		return 0
	case "help":
		topic := ""
		if len(args) > 0 {
			topic = args[0]
		}
		if err := helpTopic(os.Stdout, spec, topic); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", spec.Name, err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "%s: unknown command %q (run '%s help')\n", spec.Name, cmd, spec.Name)
	return 2
}

// usageError is a mistake in how a command was called: its help topic is
// pointed to.
type usageError struct{ error }

// finish turns a command's result into an exit code; asking for help (-h)
// prints the command's topic.
func finish(spec Spec, topic string, err error) int {
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		helpTopic(os.Stdout, spec, topic)
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(os.Stderr, "%s: %v (run '%s help %s')\n", spec.Name, err, spec.Name, topic)
		return 2
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", spec.Name, err)
	return 1
}

// homeFor is the program's folder: dir if given, else $<NAME>_HOME or ~/.<name>.
func homeFor(name, dir string) (home.Dirs, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		return home.Dirs{Root: abs}, err
	}
	return home.For(name)
}
