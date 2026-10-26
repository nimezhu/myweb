package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// Help: "myweb -h", "myweb help" and "myweb serve -h" show the overview;
// "myweb help COMMAND" and "myweb COMMAND -h" show a command's topic.

// flagGroups orders serve's flags in the overview; a flag missing here is
// listed under "Other", so none is ever left out.
var flagGroups = []struct {
	title string
	names []string
}{
	{"Files", []string{"d", "p", "global", "r", "home", "no-dav"}},
	{"Web app at /", []string{"app", "web", "web-from", "web-key", "web-version", "no-update", "web-check", "other-apps", "no-manager"}},
	{"Access", []string{"auth", "token", "cors", "trust-html", "https", "cert", "key", "tunnel"}},
	{"Safety", []string{"versions", "trash-days", "upload-max", "backup-every", "backup-keep", "backup-to"}},
	{"Running", []string{"no-tui", "no-open", "log", "new", "no-upgrade-check", "v"}},
}

// hiddenFlags have a command that shows them better.
var hiddenFlags = map[string]bool{"apps": true, "version": true}

var placeholders = map[string]string{
	"d": "DIR", "p": "PORT", "home": "DIR", "app": "NAME", "web": "DIR", "web-from": "URL",
	"web-key": "KEY", "web-version": "VERSION", "web-check": "DURATION", "token": "TOKEN",
	"cors": "ORIGINS", "cert": "FILE", "key": "FILE", "versions": "N", "trash-days": "DAYS", "upload-max": "MIB", "log": "FILE",
	"backup-every": "TIME", "backup-keep": "N", "backup-to": "DIR",
}

// noDefault are flags whose default is not worth showing (or is secret:
// -token defaults to $<NAME>_TOKEN).
var noDefault = map[string]bool{"token": true}

func overview(w io.Writer, spec Spec) {
	name := spec.Name
	if spec.Access != nil { // myweb-team and the like: their own commands
		fmt.Fprintf(w, "usage: %[1]s [serve] [flags]   (%[1]s help lists the other commands)\n\n", name)
		printFlags(w, spec)
		return
	}
	fmt.Fprintf(w, `%[1]s serves a folder of data files to web apps over HTTP, with a file manager
and a terminal UI.

usage:
  %[1]s [serve] [flags]        serve files (the default command)
  %[1]s COMMAND ...

commands:
  app          installed web apps: list, run, install, update, versions, use, remove, default
  status       the servers running now; open one in the browser, or stop it:
               status, open [WHICH], stop [WHICH | -all]
  share        links to a folder or file that open without the token:
               share PATH [-upload] [-days N], share list, share revoke ID
  service      run %[1]s in the background, started at login
  config       saved defaults for the flags below
  upgrade      install the latest release from GitHub (or -check, VERSION, -rollback)
  doctor       check the setup and say what to fix (-fix: fix what is safe to)
  backup       the home and data folder, with versions and trash, in one .zip;
               restore FILE.zip puts it back
  tls          the CA of -https's certificates; tls trust: this machine trusts it
  completion   shell completion for bash, zsh and fish
  version      print the version
  help [CMD]   this help, or a command's

`, name)
	printFlags(w, spec)
	fmt.Fprintf(w, `
Flags given here win over the saved ones (%[1]s config). Starting the same
folder and app again opens the copy that is running; -new starts another.

examples:
  %[1]s                          serve ~/.%[1]s/data and open the browser
  %[1]s -d ~/data -p 9000        serve another folder on another port
  %[1]s -web ./dist              your own web app at /, the file manager at /_myweb/
                                 (the data folder is also a network drive: WebDAV at /dav/)
  %[1]s app run myapp            an app installed with -web-from (see %[1]s help app)
  %[1]s -global -https -auth     on your network, over HTTPS
  %[1]s -tunnel                  also from anywhere, at an https://….trycloudflare.com address

-https makes a certificate for this machine (in <home>/tls). Browsers warn
until it is trusted: check that the SHA-256 they show matches the one %[1]s
prints, or trust <home>/tls/cert.pem in your system. Or serve one you have:
-cert and -key (mkcert, tailscale cert, a CA). Without -global or -tunnel,
%[1]s answers only to localhost and 127.0.0.1.
`, name)
}

// printFlags lists serve's flags by group.
func printFlags(w io.Writer, spec Spec) {
	fs, _ := newServeFlags(spec)
	fmt.Fprintln(w, "flags:")
	done := map[string]bool{}
	for _, g := range flagGroups {
		printGroup(w, fs, g.title, g.names, done)
	}
	var other []string
	fs.VisitAll(func(f *flag.Flag) {
		if !done[f.Name] && !hiddenFlags[f.Name] {
			other = append(other, f.Name)
		}
	})
	printGroup(w, fs, "Other", other, done)
}

func printGroup(w io.Writer, fs *flag.FlagSet, title string, names []string, done map[string]bool) {
	var lines []string
	for _, n := range names {
		f := fs.Lookup(n)
		if f == nil || done[n] {
			continue
		}
		done[n] = true
		left := "-" + n
		if n == "v" {
			left = "-v, -version"
			done["version"] = true
		}
		if p := placeholders[n]; p != "" {
			left += " " + p
		}
		usage := strings.ReplaceAll(f.Usage, "\n", " ")
		if n == "v" {
			usage = "print the version and exit"
		}
		if d := shownDefault(f); d != "" {
			usage += " (default " + d + ")"
		}
		lines = append(lines, formatRow(left, usage))
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s\n%s", title, strings.Join(lines, ""))
}

func shownDefault(f *flag.Flag) string {
	if noDefault[f.Name] || isBool(f) {
		return ""
	}
	switch d := f.DefValue; {
	case d == "" || d == "0" || d == "[]":
		return ""
	case strings.HasSuffix(d, "h0m0s"):
		return strings.TrimSuffix(d, "0m0s")
	default:
		return d
	}
}

// formatRow puts the text in a column, wrapped at 80 characters.
func formatRow(left, text string) string {
	const col, width = 26, 80
	var b strings.Builder
	line := "    " + left
	if len(line) >= col-1 {
		b.WriteString(line + "\n")
		line = ""
	}
	line += strings.Repeat(" ", col-len(line))
	for _, word := range strings.Fields(text) {
		if len(line)+len(word)+1 > width && strings.TrimSpace(line) != "" {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			line = strings.Repeat(" ", col)
		}
		if !strings.HasSuffix(line, " ") {
			line += " "
		}
		line += word
	}
	b.WriteString(line + "\n")
	return b.String()
}

var helpTopics = map[string]string{
	"app": `web apps installed from a distribution server (see -web-from):

  %[1]s app [list]                        list the installed apps
  %[1]s app run NAME [flags]              serve one (same as %[1]s -app NAME [flags])
  %[1]s app catalog [WORD]                the apps you can install, from the catalogs
  %[1]s app install NAME                  install one of them (app run NAME does too)
  %[1]s app install -from URL -key KEY    install any app, or update it, without serving it
  %[1]s app update [NAME]                 update one or all installed apps now
  %[1]s app versions NAME [-all]          installed versions, or with -all every published one
  %[1]s app use NAME VERSION|latest       serve that version and stop updating (installs it
                                          if needed); latest follows updates again
  %[1]s app remove NAME [-y]              delete an installed app
  %[1]s app default [NAME | -clear]       the app that plain "%[1]s" serves
  %[1]s app access NAME                   the folders an app may use
  %[1]s app allow NAME FOLDER [-write]    let it read (and with -write change) a folder
  %[1]s app deny NAME FOLDER              take a folder back
  %[1]s app catalog add URL -key KEY      also read this catalog
  %[1]s app catalog remove URL

Every app command takes -home DIR. NAME may also be the app's manifest URL.
An installed app may use only its own folders of the data folder (those its
publisher declares, else /NAME) and those you allow; "allow NAME /" gives it
everything. While it runs, the file manager is at http://localhost:PORT/_myweb/.
A catalog is a signed list of apps (apps.json, made with myweb-publish
catalog). %[1]s reads the ones added (none is built in). The file manager's
"App catalog" installs and opens them too.
`,
	"config": `saved defaults for serve's flags, kept in <home>/config.json:

  %[1]s config                            show them
  %[1]s config set NAME VALUE             e.g. config set p 9000, config set d ~/data
  %[1]s config unset NAME

NAME is a flag without its dash. Flags given on the command line win; -web,
-web-from or -app replace a saved app. Takes -home DIR.
`,
	"service": `run %[1]s in the background, now and at every login, with your own
service manager (systemd user service, macOS LaunchAgent, Windows Startup
folder); no admin rights needed:

  %[1]s service install [-as NAME] [flags]   install (again: replace the flags)
  %[1]s service uninstall [-as NAME]
  %[1]s service status [-as NAME]

flags are serve's, e.g. -app myapp -d ~/data. -as names a second service (e.g.
one per app); -dry-run shows what install or uninstall would do. Keep a token
in "%[1]s config set token …", not in the service's flags.
`,
	"status": `the %[1]s servers running now (started from this -home):

  %[1]s status [-json]          list them, numbered
  %[1]s open [WHICH]            open one in the browser (signed in)
  %[1]s stop [WHICH | -all]     stop one, or all of them

WHICH is the number status shows, a pid, a port, an app's name or a data
folder; leave it out when only one server runs. Every command takes -home DIR.
`,
	"upgrade": `replace %[1]s with a release from github.com/nimezhu/myweb/releases:

  %[1]s upgrade                 the latest release, if it is newer
  %[1]s upgrade VERSION         that release, newer or older (e.g. 0.0.2)
  %[1]s upgrade -check          only tell whether a newer release exists
  %[1]s upgrade -rollback       back to the binary the last upgrade replaced
  %[1]s upgrade -no-restart     leave installed services on the old binary

The download is checked against the release's checksums.txt and must run
before it replaces the binary; the old one is kept next to it as .old. The
services running it (%[1]s service install) are restarted; other running
servers keep the old version until restarted. A running server tells once a
day when a newer release is out (-no-upgrade-check: don't).
myweb-team and myweb-publish upgrade the same way, from the same releases.
`,
	"share": `links to a folder or file of a running server's data folder, that open
without the token, for people who have nothing else:

  %[1]s share PATH                 a link to read (a folder: browse it, or get a .zip)
  %[1]s share PATH -upload         a drop box: files can be sent into the folder, not seen;
                                 at most -max-mb (1024) in all, and -max-files (500)
  %[1]s share PATH -days 30        how long it works (default 7, at most 365)
  %[1]s share PATH -password       ask for a password (typed at the prompt, or one line
                                 on standard input)
  %[1]s share PATH -max-downloads 3   end after 3 downloads (files or zips)
  %[1]s share list                 the links, their downloads and what they received
  %[1]s share url ID               show a link again
  %[1]s share revoke ID            end one

PATH is in the data folder (/photos), or a file or folder on disk inside it.
-on WHICH picks the server when several run (see %[1]s status); -home DIR.
A link reaches only its folder: not %[1]s's own .myweb folder, nor where a
symbolic link in it leads elsewhere. Other machines can open it only when the
server listens on the network (-global).
`,
	"doctor": `check what %[1]s needs, and say what to do about what's wrong:

  %[1]s doctor                     the binary and its version, home, config, data
                                folder, port, running servers and services,
                                installed apps, catalogs (fetched and checked)
  %[1]s doctor -offline            without asking GitHub or the catalogs
  %[1]s doctor -fix                also fix what is safe to: make the home folder,
                                remove old binaries left by upgrades, restart
                                services that stopped or run an old version,
                                install an app's missing version

Without -fix it changes nothing. The exit status is 1 when something is
broken (✗); warnings (!) are things to know.
`,
	"tls": `the CA that signs -https's certificates (kept in <home>/tls):

  %[1]s tls                  where it is, its SHA-256, whether this machine trusts it
  %[1]s tls trust            add it to this machine's trusted certificates
                          (the system asks for your password; on Linux, sudo)
  %[1]s tls untrust          take it out again
  %[1]s tls trust -print     only show the commands

Other devices (phones, other computers): open /_myweb/cert of a server
started with -https, check the SHA-256, and follow the steps there. The CA
can sign only local names (localhost, .local, .lan, .internal, home.arpa and
this machine's), so its key can't stand in for websites; keep <home>/tls
private all the same.
`,
	"backup": `the home folder and the data folder in one .zip, and back:

  %[1]s backup [FILE.zip]          config, installed apps, catalogs, and the data
                                folder with its versions, trash and share links
                                (default name: %[1]s-backup-DATE-TIME.zip)
  %[1]s backup -no-files           only the data folder's versions, trash and links
  %[1]s restore FILE.zip           put it back; files that differ are kept as
                                they are and listed
  %[1]s restore FILE.zip -force    replace them
  %[1]s restore FILE.zip -dry-run  say what it would do
  %[1]s restore FILE.zip -d DIR    the data folder somewhere else

  %[1]s backup -every 1d           from now on a running %[1]s backs up by itself
                                when the last one is a day old (12h, 1w, …)
  %[1]s backup -every 1d -keep 14 -to /mnt/disk/backups
                                keep 14 (7 if not said), in that folder
                                (default ~/.%[1]s/backups: better another disk)
  %[1]s backup -every off          stop
  %[1]s backup -list               the schedule and the backups made

The data folder of the home (-home DIR) goes back into the home it is
restored to; another data folder goes back where it was, unless -d. The zip
holds the token: keep it private. Symbolic links are left out. Restore
refuses while a server runs from the home. The schedule is kept in the
config (backup-every, backup-keep, backup-to) for servers and services
started after it; %[1]s doctor says when the last backup failed or is old.
`,
	"completion": `shell completion of commands, flags, app names and versions:

  bash: add to ~/.bashrc                  source <(%[1]s completion bash)
  zsh:  add to ~/.zshrc, after compinit   source <(%[1]s completion zsh)
  fish: %[1]s completion fish > ~/.config/fish/completions/%[1]s.fish
`,
}

// helpTopic prints a command's help, or the overview for "" and "serve".
func helpTopic(w io.Writer, spec Spec, topic string) error {
	switch topic {
	case "", "serve":
		overview(w, spec)
		return nil
	case "apps":
		topic = "app"
	case "restore":
		topic = "backup"
	}
	t, ok := helpTopics[topic]
	if !ok {
		return fmt.Errorf("no help for %q; topics: app, status, share, doctor, backup, config, service, upgrade, completion", topic)
	}
	fmt.Fprintf(w, t, spec.Name)
	return nil
}

func isHelpArg(a string) bool {
	return a == "-h" || a == "-help" || a == "--help" || a == "help"
}
