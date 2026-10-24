package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/webapp"
)

// "service" starts the server in the background at login, with the user's
// own service manager: a systemd user unit (Linux), a LaunchAgent (macOS) or
// a launcher in the Startup folder (Windows). Nothing needs admin rights.

// runCmd runs a service manager command; replaced in tests.
var runCmd = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// service is one background server: its name and the serve flags it runs with.
type service struct {
	goos    string
	name    string // e.g. myweb, or myweb-myapp
	exe     string
	args    []string // full command line after exe
	homeDir string
}

func serviceCmd(spec Spec, args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	for _, a := range args {
		if isHelpArg(a) && a != "help" {
			return flag.ErrHelp
		}
	}
	if sub == "" || sub == "help" {
		return flag.ErrHelp
	}
	// options of the service command itself; the rest are serve flags
	var as, homeDir string
	dry := false
	var rest []string
	for i := 0; i < len(args); i++ {
		name, v, hasV := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		value := func() string {
			if !hasV && i+1 < len(args) {
				i++
				v = args[i]
			}
			return v
		}
		switch {
		case !strings.HasPrefix(args[i], "-"):
			rest = append(rest, args[i])
		case name == "as":
			as = value()
		case name == "home": // the service always runs with an explicit -home
			homeDir = value()
		case name == "dry-run":
			dry = true
		default:
			rest = append(rest, args[i])
		}
	}
	if as == "" {
		as = spec.Name
	}
	if !webapp.ValidName(as) {
		return fmt.Errorf("invalid service name %q", as)
	}
	dirs, err := homeFor(spec.Name, homeDir)
	if err != nil {
		return err
	}
	switch sub {
	case "install":
		rest, err := checkServeArgs(spec, rest)
		if err != nil {
			return err
		}
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return err
		}
		if strings.Contains(exe, "go-build") {
			return errors.New("this is a temporary build (go run); install the binary first, e.g. go install, and run the service command with it")
		}
		s := newService(runtime.GOOS, spec.Name, as, exe, dirs, rest)
		return s.install(dry)
	case "uninstall", "remove":
		return newService(runtime.GOOS, spec.Name, as, "", dirs, nil).uninstall(dry)
	case "status":
		return newService(runtime.GOOS, spec.Name, as, "", dirs, nil).status()
	}
	return usageError{fmt.Errorf("unknown command \"service %s\"", sub)}
}

// checkServeArgs refuses flags serve would refuse, before they are saved, and
// makes paths absolute: the service doesn't run in the current folder.
func checkServeArgs(spec Spec, args []string) ([]string, error) {
	fs, f := newServeFlags(spec)
	fs.SetOutput(new(strings.Builder))
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("serve flags: %v", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected %q: give serve's flags, e.g. -app myapp -d ~/data", fs.Arg(0))
	}
	if *f.version || f.webFlags.List {
		return nil, errors.New("-v and -apps print and exit; they can't run as a service")
	}
	tokenGiven := false
	fs.Visit(func(fl *flag.Flag) { tokenGiven = tokenGiven || fl.Name == "token" })
	if tokenGiven {
		return nil, fmt.Errorf("the service file is readable by others: keep the token in the private config instead,\n  %s config set token TOKEN", spec.Name)
	}
	var out []string
	var err error
	fs.Visit(func(fl *flag.Flag) {
		v := fl.Value.String()
		switch fl.Name {
		case "d", "web", "log":
			v, err = absPath(v, err)
		case "web-key": // a key, or a file holding it
			if _, statErr := os.Stat(v); statErr == nil {
				v, err = absPath(v, err)
			}
		}
		out = append(out, "-"+fl.Name+"="+v)
	})
	return out, err
}

func absPath(p string, prev error) (string, error) {
	abs, err := filepath.Abs(p)
	if prev != nil {
		err = prev
	}
	return abs, err
}

func newService(goos, program, name, exe string, dirs home.Dirs, serveArgs []string) *service {
	args := []string{"-no-tui", "-no-open", "-home", dirs.Root}
	hasLog := false
	for _, a := range serveArgs {
		switch strings.SplitN(strings.TrimLeft(a, "-"), "=", 2)[0] {
		case "no-tui", "no-open":
			continue
		case "log":
			hasLog = true
		}
		args = append(args, a)
	}
	// systemd keeps the output in the journal; elsewhere it would be lost
	if goos != "linux" && !hasLog {
		args = append(args, "-log", filepath.Join(dirs.Root, "logs", name+".log"))
	}
	return &service{goos: goos, name: name, exe: exe, args: args, homeDir: dirs.Root}
}

// file is where the service definition goes.
func (s *service) file() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch s.goos {
	case "linux":
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(h, ".config")
		}
		return filepath.Join(cfg, "systemd", "user", s.name+".service"), nil
	case "darwin":
		return filepath.Join(h, "Library", "LaunchAgents", s.label()+".plist"), nil
	case "windows":
		app := os.Getenv("APPDATA")
		if app == "" {
			app = filepath.Join(h, "AppData", "Roaming")
		}
		return filepath.Join(app, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", s.name+".vbs"), nil
	}
	return "", fmt.Errorf("services are not supported on %s; start it from your system's startup settings", s.goos)
}

func (s *service) label() string { return "com.github.nimezhu." + s.name }

// definition renders the service file.
func (s *service) definition() string {
	switch s.goos {
	case "linux":
		cmd := []string{systemdQuote(s.exe)}
		for _, a := range s.args {
			cmd = append(cmd, systemdQuote(a))
		}
		return fmt.Sprintf(`[Unit]
Description=%s (myweb service)
After=network-online.target

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, s.name, strings.Join(cmd, " "))
	case "darwin":
		var b strings.Builder
		for _, a := range append([]string{s.exe}, s.args...) {
			b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
		}
		logFile := xmlEscape(filepath.Join(s.homeDir, "logs", s.name+".out"))
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, s.label(), b.String(), logFile, logFile)
	case "windows":
		cmd := []string{windowsQuote(s.exe)}
		for _, a := range s.args {
			cmd = append(cmd, windowsQuote(a))
		}
		line := strings.ReplaceAll(strings.Join(cmd, " "), `"`, `""`)
		return "' " + s.name + " (myweb service): starts the server hidden at sign-in\r\n" +
			`CreateObject("WScript.Shell").Run "` + line + `", 0, False` + "\r\n"
	}
	return ""
}

// commands are what install runs after writing the file.
func (s *service) commands(file string) [][]string {
	switch s.goos {
	case "linux":
		return [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", s.name + ".service"},
			{"systemctl", "--user", "restart", s.name + ".service"},
		}
	case "darwin":
		uid := strconv.Itoa(os.Getuid())
		return [][]string{
			{"launchctl", "bootout", "gui/" + uid + "/" + s.label()}, // may fail: not loaded yet
			{"launchctl", "bootstrap", "gui/" + uid, file},
		}
	case "windows":
		return [][]string{{"wscript", file}}
	}
	return nil
}

func (s *service) install(dry bool) error {
	file, err := s.file()
	if err != nil {
		return err
	}
	def := s.definition()
	cmds := s.commands(file)
	if dry {
		fmt.Printf("would write %s:\n\n%s\nand run:\n", file, def)
		for _, c := range cmds {
			fmt.Println("  " + strings.Join(c, " "))
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if s.goos != "linux" {
		os.MkdirAll(filepath.Join(s.homeDir, "logs"), 0o755)
	}
	if err := os.WriteFile(file, []byte(def), 0o644); err != nil {
		return err
	}
	for i, c := range cmds {
		out, err := runCmd(c[0], c[1:]...)
		if err != nil && !(s.goos == "darwin" && i == 0) {
			return fmt.Errorf("%s: %v\n%s", strings.Join(c, " "), err, out)
		}
	}
	fmt.Printf("installed %s: it runs now and starts at every login\n  %s\n", s.name, file)
	switch s.goos {
	case "linux":
		fmt.Printf("logs: journalctl --user -u %s -f\n", s.name)
		if out, _ := runCmd("loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger"); !strings.Contains(out, "Linger=yes") {
			fmt.Println("it stops when you log out; to keep it running (and start at boot): loginctl enable-linger")
		}
	default:
		fmt.Printf("logs: %s\n", filepath.Join(s.homeDir, "logs"))
	}
	fmt.Println("open it: run myweb with the same folder and app; it opens the running copy")
	return nil
}

func (s *service) uninstall(dry bool) error {
	file, err := s.file()
	if err != nil {
		return err
	}
	var cmds [][]string
	switch s.goos {
	case "linux":
		cmds = [][]string{{"systemctl", "--user", "disable", "--now", s.name + ".service"}}
	case "darwin":
		cmds = [][]string{{"launchctl", "bootout", "gui/" + strconv.Itoa(os.Getuid()) + "/" + s.label()}}
	}
	if dry {
		fmt.Printf("would run:\n")
		for _, c := range cmds {
			fmt.Println("  " + strings.Join(c, " "))
		}
		fmt.Printf("and remove %s\n", file)
		return nil
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return fmt.Errorf("no service %s (%s)", s.name, file)
	}
	for _, c := range cmds {
		runCmd(c[0], c[1:]...) // stopping a service that isn't running is fine
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	if s.goos == "linux" {
		runCmd("systemctl", "--user", "daemon-reload")
	}
	fmt.Printf("removed %s\n", s.name)
	if s.goos == "windows" {
		fmt.Println("the copy running now stops at sign-out (or end myweb in Task Manager)")
	}
	return nil
}

func (s *service) status() error {
	file, err := s.file()
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		fmt.Printf("%s: not installed\n", s.name)
		return nil
	}
	fmt.Printf("%s: installed (%s)\n", s.name, file)
	switch s.goos {
	case "linux":
		out, _ := runCmd("systemctl", "--user", "status", "--no-pager", "-n", "5", s.name+".service")
		fmt.Println(out)
	case "darwin":
		out, err := runCmd("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+s.label())
		if err != nil {
			fmt.Println("not loaded")
		} else {
			for _, l := range strings.Split(out, "\n") {
				if t := strings.TrimSpace(l); strings.HasPrefix(t, "state =") || strings.HasPrefix(t, "pid =") || strings.HasPrefix(t, "last exit code") {
					fmt.Println("  " + t)
				}
			}
		}
	}
	return nil
}

// systemdQuote quotes one ExecStart argument.
func systemdQuote(a string) string {
	a = strings.ReplaceAll(a, "%", "%%")
	if a != "" && !strings.ContainsAny(a, " \t\"'\\;$") {
		return a
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`)
	return `"` + r.Replace(a) + `"`
}

// windowsQuote quotes one argument for a Windows command line.
func windowsQuote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range a {
		switch c {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteRune(c)
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// installedServices lists this user's services that run exe.
func installedServices(goos, exe string) []*service {
	probe := &service{goos: goos, name: "x"}
	file, err := probe.file()
	if err != nil {
		return nil
	}
	var pattern, prefix, suffix, runs string
	switch goos {
	case "linux":
		pattern, suffix, runs = "*.service", ".service", "ExecStart="+systemdQuote(exe)+" "
	case "darwin":
		prefix = strings.TrimSuffix(probe.label(), "x")
		pattern, suffix, runs = prefix+"*.plist", ".plist", "<string>"+xmlEscape(exe)+"</string>"
	case "windows":
		pattern, suffix, runs = "*.vbs", ".vbs", `Run "`+strings.ReplaceAll(windowsQuote(exe), `"`, `""`)+" "
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(file), pattern))
	var out []*service
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), runs) {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), prefix), suffix)
		out = append(out, &service{goos: goos, name: name, exe: exe})
	}
	return out
}

// restart restarts the service, if it runs, so it runs the binary now at
// its exe. running are the servers that run; on Windows, which has no
// service manager to ask, the one the service started is stopped first.
func (s *service) restart(running []server) (bool, error) {
	switch s.goos {
	case "linux":
		if out, _ := runCmd("systemctl", "--user", "is-active", s.name+".service"); out != "active" {
			return false, nil
		}
		if out, err := runCmd("systemctl", "--user", "restart", s.name+".service"); err != nil {
			return false, fmt.Errorf("%v\n%s", err, out)
		}
		return true, nil
	case "darwin":
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + s.label()
		if _, err := runCmd("launchctl", "print", target); err != nil {
			return false, nil // not loaded
		}
		if out, err := runCmd("launchctl", "kickstart", "-k", target); err != nil {
			return false, fmt.Errorf("%v\n%s", err, out)
		}
		return true, nil
	case "windows":
		file, err := s.file()
		if err != nil {
			return false, err
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return false, err
		}
		stopped := false
		for _, r := range running {
			if len(r.Args) > 0 && (&service{goos: s.goos, name: s.name, exe: s.exe, args: r.Args}).definition() == string(b) {
				if err := stopServer(r); err != nil {
					return false, err
				}
				stopped = true
			}
		}
		if !stopped {
			return false, nil
		}
		if out, err := runCmd("wscript", file); err != nil {
			return false, fmt.Errorf("%v\n%s", err, out)
		}
		return true, nil
	}
	return false, nil
}
