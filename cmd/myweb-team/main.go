// myweb-team is myweb for several people: each signs in with a personal
// link, has a private folder (/users/<name>), and folders can be shared.
//
//	myweb-team user add alice -role admin     prints alice's personal sign-in link
//	myweb-team user list | remove | reset | role
//	myweb-team share /projects/x -read bob,vic -write alice
//	myweb-team unshare /projects/x
//	myweb-team folders
//	myweb-team [serve] [myweb flags]          runs the server (-h for flags)
//
// Users and folders live in ~/.myweb-team/team.json ($MYWEB_TEAM_HOME, or -home); a
// running server picks up changes within a second.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/nimezhu/myweb/cli"
	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/team"
)

const name = "myweb-team"

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd != "" && cmd != "serve" {
		args, homeDir = takeHome(args)
	}
	var err error
	switch cmd {
	case "", "serve":
		os.Exit(serve(args))
	case "user", "users":
		err = userCmd(args)
	case "share":
		err = shareCmd(args)
	case "unshare":
		err = unshareCmd(args)
	case "folders":
		err = foldersCmd()
	case "help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q (try: user, share, unshare, folders, serve, help)", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`myweb-team: myweb for several people, with personal sign-in links,
private folders (/users/<name>) and shared folders.

  myweb-team user add NAME [-role member|viewer|admin] [-url http://HOST:PORT]
  myweb-team user list
  myweb-team user remove NAME
  myweb-team user reset NAME           new sign-in link (the old one stops working)
  myweb-team user role NAME ROLE
  myweb-team share PATH [-read a,b|*] [-write c]
  myweb-team unshare PATH
  myweb-team folders
  myweb-team [serve] [flags]           run the server; myweb-team serve -h lists flags

Users and folders are kept in ~/.myweb-team/team.json (or $MYWEB_TEAM_HOME,
or -home DIR, which every command takes);
a running server picks up changes within a second.
`)
}

// homeDir is -home for the user and share commands; serve parses its own.
var homeDir string

// takeHome removes -home DIR (or -home=DIR) from anywhere in args.
func takeHome(args []string) (rest []string, dir string) {
	for i := 0; i < len(args); i++ {
		a := strings.TrimPrefix(args[i], "-")
		switch {
		case a == "-home" || a == "home":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-home=") || strings.HasPrefix(a, "home="):
			dir = a[strings.Index(a, "=")+1:]
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, dir
}

func teamFile() (string, error) {
	if homeDir != "" {
		abs, err := filepath.Abs(homeDir)
		return filepath.Join(abs, "team.json"), err
	}
	d, err := home.For(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Root, "team.json"), nil
}

// edit loads team.json, applies f and saves it.
func edit(f func(c *team.Config) error) error {
	file, err := teamFile()
	if err != nil {
		return err
	}
	c, err := team.Load(file)
	if err != nil {
		return err
	}
	if err := f(c); err != nil {
		return err
	}
	return c.Save(file)
}

func serve(args []string) int {
	return cli.Serve(cli.Spec{
		Name: name,
		Args: args,
		Access: func(dirs home.Dirs, root string) (myserver.Access, []console.Row, error) {
			file := filepath.Join(dirs.Root, "team.json")
			c, err := team.Load(file)
			if err != nil {
				return nil, nil, err
			}
			if len(c.Users) == 0 {
				return nil, nil, fmt.Errorf("no users yet; add yourself first:\n  myweb-team user add <your name> -role admin")
			}
			a, err := team.Open(file, root)
			if err != nil {
				return nil, nil, err
			}
			return a, []console.Row{{Label: "Team", Func: a.Summary}}, nil
		},
	})
}

func signInLink(base, token string) string {
	return strings.TrimRight(base, "/") + "/?token=" + url.QueryEscape(token)
}

func userCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		role := fs.String("role", team.Member, "member, viewer or admin")
		base := fs.String("url", "http://127.0.0.1:8433", "the server's address, for the sign-in link")
		n, err := oneName(fs, rest, "user add NAME")
		if err != nil {
			return err
		}
		var tok string
		if err := edit(func(c *team.Config) (err error) { tok, err = c.AddUser(n, *role); return }); err != nil {
			return err
		}
		fmt.Printf("added %s (%s); their folder is %s\n\npersonal sign-in link (give it only to %s; shown once):\n  %s\n\nscripts can send:  Authorization: Bearer %s\n",
			n, *role, homeOrAll(n, *role), n, signInLink(*base, tok), tok)
		return nil
	case "reset":
		fs := flag.NewFlagSet("user reset", flag.ExitOnError)
		base := fs.String("url", "http://127.0.0.1:8433", "the server's address, for the sign-in link")
		n, err := oneName(fs, rest, "user reset NAME")
		if err != nil {
			return err
		}
		var tok string
		if err := edit(func(c *team.Config) (err error) { tok, err = c.ResetToken(n); return }); err != nil {
			return err
		}
		fmt.Printf("new sign-in link for %s (the old one no longer works):\n  %s\n", n, signInLink(*base, tok))
		return nil
	case "remove", "rm":
		n, err := oneName(flag.NewFlagSet("user remove", flag.ExitOnError), rest, "user remove NAME")
		if err != nil {
			return err
		}
		if err := edit(func(c *team.Config) error { return c.RemoveUser(n) }); err != nil {
			return err
		}
		fmt.Printf("removed %s; their files stay in %s\n", n, team.HomeOf(n))
		return nil
	case "role":
		if len(rest) != 2 {
			return fmt.Errorf("usage: user role NAME member|viewer|admin")
		}
		return edit(func(c *team.Config) error { return c.SetRole(rest[0], rest[1]) })
	case "list", "ls":
		file, err := teamFile()
		if err != nil {
			return err
		}
		c, err := team.Load(file)
		if err != nil {
			return err
		}
		if len(c.Users) == 0 {
			fmt.Println("no users yet: myweb-team user add <name> -role admin")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tROLE\tFOLDER\tADDED")
		for _, u := range c.Users {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Name, u.Role, homeOrAll(u.Name, u.Role), u.Created.Format("2006-01-02"))
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown: user %s (add, list, remove, reset, role)", sub)
}

func homeOrAll(n, role string) string {
	if role == team.Admin {
		return "(all folders)"
	}
	return team.HomeOf(n)
}

func oneName(fs *flag.FlagSet, args []string, usage string) (string, error) {
	// accept the name before or after the flags
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		fs.Parse(args[1:])
		return args[0], nil
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		return "", fmt.Errorf("usage: %s", usage)
	}
	return fs.Arg(0), nil
}

func list(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func shareCmd(args []string) error {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	read := fs.String("read", "", "users who may read, comma-separated; * for everyone signed in")
	write := fs.String("write", "", "users who may read and write")
	p, err := oneName(fs, args, "share PATH [-read a,b|*] [-write c]")
	if err != nil {
		return err
	}
	if *read == "" && *write == "" {
		return fmt.Errorf("give -read and/or -write")
	}
	if err := edit(func(c *team.Config) error { return c.Share(p, list(*read), list(*write)) }); err != nil {
		return err
	}
	fmt.Printf("shared %s: read %s, write %s\n", p, orNone(*read), orNone(*write))
	return nil
}

func unshareCmd(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: unshare PATH")
	}
	found := false
	if err := edit(func(c *team.Config) error { found = c.Unshare(args[0]); return nil }); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s was not shared", args[0])
	}
	fmt.Printf("%s is no longer shared\n", args[0])
	return nil
}

func foldersCmd() error {
	file, err := teamFile()
	if err != nil {
		return err
	}
	c, err := team.Load(file)
	if err != nil {
		return err
	}
	if len(c.Folders) == 0 {
		fmt.Println("no shared folders (each user has their own /users/<name>)")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FOLDER\tREAD\tWRITE")
	for _, f := range c.Folders {
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Path, orNone(strings.Join(f.Read, ",")), orNone(strings.Join(f.Write, ",")))
	}
	return w.Flush()
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
