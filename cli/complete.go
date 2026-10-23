package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nimezhu/myweb/webapp"
)

// Shell completion: "myweb completion bash|zsh|fish" prints a script that
// asks "myweb __complete WORDS…" (the words before the one being completed)
// for candidates. No candidates means: complete file names.

var completionScripts = map[string]string{
	"bash": `# bash completion for {{name}}; load it with: source <({{name}} completion bash)
_{{fn}}_complete() {
	local cur="${COMP_WORDS[COMP_CWORD]}" IFS=$'\n' words
	words=$({{name}} __complete "${COMP_WORDS[@]:1:COMP_CWORD-1}" 2>/dev/null)
	if [ -n "$words" ]; then
		COMPREPLY=($(compgen -W "$words" -- "$cur"))
	else
		COMPREPLY=()
	fi
}
complete -o default -o bashdefault -F _{{fn}}_complete {{name}}
`,
	"zsh": `#compdef {{name}}
# zsh completion for {{name}}; load it with: source <({{name}} completion zsh)
_{{fn}}() {
	local -a c
	c=("${(@f)$({{name}} __complete "${(@)words[2,CURRENT-1]}" 2>/dev/null)}")
	c=(${c:#})
	if (( ${#c} )); then compadd -a c; else _files; fi
}
compdef _{{fn}} {{name}}
`,
	"fish": `# fish completion for {{name}}; load it with: {{name}} completion fish | source
function __{{fn}}_complete
	set -l c ({{name}} __complete (commandline -opc)[2..-1] 2>/dev/null)
	if test (count $c) -gt 0
		printf '%s\n' $c
	else
		__fish_complete_path (commandline -ct)
	end
end
complete -c {{name}} -f -a '(__{{fn}}_complete)'
`,
}

func completionCmd(spec Spec, args []string, w io.Writer) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		return flag.ErrHelp
	}
	if len(args) != 1 || completionScripts[args[0]] == "" {
		return usageError{fmt.Errorf("completion takes bash, zsh or fish")}
	}
	fn := strings.NewReplacer("-", "_", ".", "_").Replace(spec.Name)
	fmt.Fprint(w, strings.NewReplacer("{{name}}", spec.Name, "{{fn}}", fn).Replace(completionScripts[args[0]]))
	return nil
}

var (
	topCommands = []string{"serve", "app", "status", "open", "stop", "config", "service", "upgrade", "completion", "version", "help"}
	appCommands = []string{"list", "run", "install", "update", "versions", "use", "remove", "default", "catalog"}
)

// completions returns the candidates after words (the command line so far,
// without the program name and the word being completed).
func completions(spec Spec, words []string) []string {
	fs, _ := newServeFlags(spec)
	serveFlags := flagNames(fs)
	home := ""
	for i, w := range words {
		if (w == "-home" || w == "--home") && i+1 < len(words) {
			home = words[i+1]
		}
	}
	catalogApps := func() []string { // from the cached catalogs; never waits long
		dirs, err := homeFor(spec.Name, home)
		if err != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		list, _ := catalogsFor(dirs).Apps(ctx, false)
		var names []string
		for _, a := range list {
			names = append(names, a.Name)
		}
		return names
	}
	apps := func() []string {
		dirs, err := homeFor(spec.Name, home)
		if err != nil {
			return nil
		}
		list, _ := webapp.InstalledApps(dirs.Apps())
		var names []string
		for _, a := range list {
			names = append(names, a.Name)
		}
		return names
	}

	// the value of a flag
	if n := len(words); n > 0 {
		prev := strings.TrimLeft(words[n-1], "-")
		if strings.HasPrefix(words[n-1], "-") && !strings.Contains(prev, "=") {
			switch prev {
			case "app":
				return apps()
			case "as", "from", "key": // values of the app and service commands' own flags
				return nil
			}
			if f := fs.Lookup(prev); f != nil && !isBool(f) {
				return nil // a path or a value: files
			}
		}
	}
	if len(words) == 0 {
		return append(append([]string{}, topCommands...), serveFlags...)
	}
	// positional words after the command and subcommand, without flags
	positional := func(from int) []string {
		var out []string
		for i := from; i < len(words); i++ {
			if strings.HasPrefix(words[i], "-") {
				if !strings.Contains(words[i], "=") && strings.TrimLeft(words[i], "-") == "home" {
					i++
				}
				continue
			}
			out = append(out, words[i])
		}
		return out
	}
	switch words[0] {
	case "serve":
		return serveFlags
	case "app", "apps":
		if len(words) == 1 {
			return appCommands
		}
		pos := positional(2)
		switch words[1] {
		case "run":
			if len(pos) == 0 {
				return apps()
			}
			return serveFlags
		case "use":
			switch len(pos) {
			case 0:
				return apps()
			case 1:
				dirs, err := homeFor(spec.Name, home)
				if err != nil {
					return nil
				}
				a, err := webapp.FindApp(dirs.Apps(), pos[0])
				if err != nil {
					return nil
				}
				return append(append([]string{}, a.Versions...), "latest")
			}
		case "versions":
			if len(pos) == 0 {
				return apps()
			}
			return []string{"-all", "-home"}
		case "remove", "rm":
			if len(pos) == 0 {
				return apps()
			}
			return []string{"-y", "-home"}
		case "default":
			if len(pos) == 0 {
				return append(apps(), "-clear")
			}
		case "update":
			return apps()
		case "install":
			if len(pos) == 0 {
				return append(catalogApps(), "-from", "-key", "-home")
			}
			return []string{"-from", "-key", "-home"}
		case "catalog":
			if len(pos) == 0 {
				return []string{"add", "remove", "-refresh", "-home"}
			}
			if pos[0] == "add" {
				return []string{"-key"}
			}
			return nil
		case "list", "ls":
			return []string{"-home"}
		}
		return []string{"-home"}
	case "config":
		if len(words) == 1 {
			return []string{"set", "unset", "-home"}
		}
		if len(words) == 2 {
			var keys []string
			if words[1] == "set" {
				fs.VisitAll(func(f *flag.Flag) {
					if !notInConfig[f.Name] {
						keys = append(keys, f.Name)
					}
				})
			} else if words[1] == "unset" {
				if dirs, err := homeFor(spec.Name, home); err == nil {
					c, _ := loadConfig(dirs)
					keys = c.keys()
				}
			}
			return keys
		}
		return nil
	case "service":
		if len(words) == 1 {
			return []string{"install", "uninstall", "status"}
		}
		if words[1] == "install" {
			return append([]string{"-as", "-dry-run"}, serveFlags...)
		}
		return []string{"-as", "-dry-run"}
	case "upgrade":
		return []string{"-check", "-rollback", "-home"}
	case "status", "ps":
		return []string{"-json", "-home"}
	case "open", "stop":
		var c []string
		if dirs, err := homeFor(spec.Name, home); err == nil {
			for i, s := range runningServers(dirs) {
				c = append(c, strconv.Itoa(i+1))
				if s.App != "" {
					c = append(c, s.App)
				}
			}
		}
		if words[0] == "stop" {
			c = append(c, "-all")
		}
		return append(c, "-home")
	case "completion":
		if len(words) == 1 {
			return []string{"bash", "zsh", "fish"}
		}
		return nil
	}
	if strings.HasPrefix(words[0], "-") {
		return serveFlags
	}
	return nil
}

func flagNames(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, "-"+f.Name) })
	sort.Strings(names)
	return names
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
