// Package cli is the command line of a myweb server: flags, folders, the
// console, and starting and stopping the server. myweb and myweb-team are
// thin wrappers around Serve.
package cli

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/manager"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/plugins"
	"github.com/nimezhu/myweb/webapp"
)

// Spec describes a program built on myweb.
type Spec struct {
	Name string   // e.g. "myweb": messages, the ~/.<name> folder, <NAME>_* variables
	Args []string // command-line arguments, without the program name

	// Access, if set, makes the server multi-user: it is called with the
	// program's folder and the data folder once flags are parsed, and may add
	// console rows. The single-token flags (-auth, -token) are not offered then.
	Access func(dirs home.Dirs, root string) (myserver.Access, []console.Row, error)

	// NoServers: the program runs no servers (myweb-publish), so "upgrade"
	// has no services or servers to restart.
	NoServers bool
}

// serveFlags are serve's command-line options.
type serveFlags struct {
	port                              *int
	global, readOnly                  *bool
	homeDir, directory, web           *string
	noManager, noTUI, noOpen, newCopy *bool
	noUpgradeCheck, otherApps, noDAV  *bool
	trustHTML, https, tunnel          *bool
	certFile, keyFile                 *string
	auth                              *bool   // nil when spec.Access is set
	token                             *string // nil when spec.Access is set
	versions, trashDays               *int
	uploadMax                         *int64
	logFile                           *string
	backupEvery                       *every
	backupKeep                        *int
	backupTo                          *string
	cors                              myserver.OriginList
	version                           *bool
	webFlags                          *webapp.Flags
}

func newServeFlags(spec Spec) (*flag.FlagSet, *serveFlags) {
	name := spec.Name
	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f := &serveFlags{}
	f.port = fs.Int("p", 8433, "port to serve on (next free port is used if taken)")
	f.global = fs.Bool("global", false, "listen on all interfaces (visible to your network)")
	f.https = fs.Bool("https", false, "serve HTTPS, with a certificate made for this machine (kept in <home>/tls;\nbrowsers warn until you trust it)")
	f.certFile = fs.String("cert", "", "serve HTTPS with this certificate (PEM; e.g. from mkcert or tailscale cert)")
	f.keyFile = fs.String("key", "", "the private key of -cert (PEM)")
	f.tunnel = fs.Bool("tunnel", false, "also reach it from anywhere at an https://….trycloudflare.com address (needs\ncloudflared; turns on -auth)")
	f.readOnly = fs.Bool("r", false, "read only (can be toggled in the TUI with 'r')")
	f.homeDir = fs.String("home", "", fmt.Sprintf("%s's own folder, holding data/ and apps/ (default $%s_HOME, or ~/.%s)", name, env, name))
	f.directory = fs.String("d", "", "the folder of data files to serve (default <home>/data;\n-d . for the current folder)")
	f.web = fs.String("web", "", "serve this web app at / (the file manager stays at "+myserver.ManagerPath+")")
	f.noManager = fs.Bool("no-manager", false, "don't serve the built-in file manager")
	f.trustHTML = fs.Bool("trust-html", false, "let HTML files in the data folder run scripts as this server (only\nfiles you trust; not while an installed app runs)")
	f.noDAV = fs.Bool("no-dav", false, "don't serve the data folder as a network drive (WebDAV at /dav/)")
	f.noTUI = fs.Bool("no-tui", false, "plain log output instead of the interactive TUI")
	f.noOpen = fs.Bool("no-open", false, "do not open the browser on start")
	f.newCopy = fs.Bool("new", false, "start another copy even if one already serves this folder and app")
	f.noUpgradeCheck = fs.Bool("no-upgrade-check", false, fmt.Sprintf("don't look for a newer %s release (checked once a day)", name))
	if spec.Access == nil {
		f.auth = fs.Bool("auth", false, "require an access token for writes and commands (random per start unless -token is given)")
		f.token = fs.String("token", os.Getenv(env+"_TOKEN"), fmt.Sprintf("access token; setting it (or $%s_TOKEN) turns on -auth", env))
	}
	f.versions = fs.Int("versions", 20, "previous versions kept per file on overwrite (0: off)")
	f.trashDays = fs.Int("trash-days", 30, "days deleted files stay in the trash (0: delete immediately)")
	f.uploadMax = fs.Int64("upload-max", 100, "largest file accepted by an upload, in MiB")
	f.logFile = fs.String("log", "", "also append requests and messages to this file (JSON lines)")
	f.backupEvery = new(every)
	fs.Var(f.backupEvery, "backup-every", "back up the home and data folder this often, e.g. 1d (off: never)")
	f.backupKeep = fs.Int("backup-keep", 7, "automatic backups kept")
	f.backupTo = fs.String("backup-to", "", "where automatic backups go (default <home>/backups)")
	fs.Var(&f.cors, "cors", fmt.Sprintf("extra browser origins allowed to call the API, comma-separated or repeated\n(e.g. https://app.example.com); also $%s_CORS", env))
	f.version = fs.Bool("version", false, "print version and exit")
	fs.BoolVar(f.version, "v", false, "print version and exit (shorthand)")
	f.webFlags = webapp.AddFlags(fs)
	f.otherApps = fs.Bool("other-apps", true, "also serve the other installed apps, each on a port of its own (on;\noff with -web, a local build, unless given; -other-apps=false serves only the app at /)")
	fs.Usage = func() { fmt.Fprintf(fs.Output(), "run '%s -h' for help\n", name) }
	return fs, f
}

// Serve runs the server until it is stopped and returns the exit code.
func Serve(spec Spec) int {
	name := spec.Name
	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	fail := func(err error) int {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 1
	}

	fs, f := newServeFlags(spec)
	port, global, readOnly, homeDir, directory, web := f.port, f.global, f.readOnly, f.homeDir, f.directory, f.web
	noManager, noTUI, noOpen, auth, token := f.noManager, f.noTUI, f.noOpen, f.auth, f.token
	versions, trashDays, uploadMax, logFile, version, webFlags := f.versions, f.trashDays, f.uploadMax, f.logFile, f.version, f.webFlags
	for _, a := range spec.Args {
		if a != "help" && isHelpArg(a) {
			overview(os.Stdout, spec)
			return 0
		}
	}
	if err := fs.Parse(spec.Args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if *version {
		fmt.Println(name, myserver.VERSION)
		return 0
	}
	dirs, err := homeFor(name, *homeDir)
	if err != nil {
		return fail(err)
	}
	cfg, err := loadConfig(dirs)
	if err != nil {
		return fail(err)
	}
	if os.Getenv(env+"_TOKEN") != "" {
		delete(cfg, "token") // the variable beats the file, as for the flag's default
	}
	if err := cfg.apply(fs, configFile(dirs)); err != nil {
		return fail(err)
	}
	if v := os.Getenv(env + "_CORS"); v != "" {
		if err := f.cors.Set(v); err != nil {
			return fail(fmt.Errorf("%s_CORS: %w", env, err))
		}
	}
	if *web != "" && (webFlags.From != "" || webFlags.App != "") {
		return fail(fmt.Errorf("use -web, -web-from or -app, not several"))
	}
	if *web != "" && !flagSet(fs, "other-apps") {
		*f.otherApps = false // a local build at /: just it, unless -other-apps says otherwise
	}

	if (*f.certFile == "") != (*f.keyFile == "") {
		return fail(fmt.Errorf("-cert and -key go together"))
	}

	if webFlags.List {
		if err := printApps(name, dirs); err != nil {
			return fail(err)
		}
		return 0
	}
	createdData := false
	if *directory == "" {
		if *directory, createdData, err = dirs.EnsureData(); err != nil {
			return fail(err)
		}
	}
	root, err := filepath.Abs(*directory)
	if err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("root directory %q does not exist or is not a directory", *directory))
	}

	var webFS, managerFS http.FileSystem
	if len(*web) > 0 {
		if fi, err := os.Stat(*web); err != nil || !fi.IsDir() {
			return fail(fmt.Errorf("web directory %q does not exist or is not a directory", *web))
		}
		webFS = http.Dir(*web)
	}
	if !*noManager {
		managerFS = manager.FS()
	}

	// -web-from: install the app from a distribution server and keep it updated
	var srv *myserver.Server
	var app *webapp.Installer
	var mountTeamApp func() // myweb-team: serve the app on its port, once installed
	if webFlags.App != "" { // an app from a catalog, not installed yet
		if err := ensureInstalled(name, dirs, webFlags.App); err != nil {
			return fail(err)
		}
	}
	app, err = webFlags.Installer(myserver.VERSION, dirs.Apps(), func(n, v string) {
		makeFolders(app, root) // a new version may use new folders
		if mountTeamApp != nil {
			mountTeamApp() // just installed: now it has a name and pages
		}
		if webFlags.App == "" && len(app.Installed()) == 1 { // just installed with -web-from
			log.Printf("app: next time, start it with -app %s (or %s app run %s)", n, name, n)
		}
		srv.Broadcast(map[string]string{"type": "app-updated", "name": n, "version": v, "notes": app.Status().Notes[v]})
	})
	if err != nil {
		return fail(err)
	}
	// myweb-team: the app gets a port of its own (below), so its pages and the
	// file manager are different sites to the browser
	teamApp := app != nil && spec.Access != nil
	if app != nil && !teamApp {
		webFS = app.FS()
	}

	// the same folder and app already being served: open that copy
	source := "files"
	switch {
	case webFlags.From != "":
		source = "app:" + webFlags.From
	case *web != "":
		abs, _ := filepath.Abs(*web)
		source = "web:" + abs
	}
	runFile := instanceFile(dirs, root, source)
	other := running(runFile)
	if other != nil && !*f.newCopy {
		fmt.Printf("%s is already serving %s at %s (pid %d); ", name, root, other.URL, other.PID)
		if *noOpen || other.URL == "" {
			fmt.Println("-new starts another copy")
			return 0
		}
		fmt.Println("opening it (-new starts another copy)")
		if err := console.OpenBrowser(other.URL); err != nil {
			return fail(fmt.Errorf("could not open browser: %w", err))
		}
		return 0
	}

	var access myserver.Access
	var rows []console.Row
	if spec.Access != nil {
		if access, rows, err = spec.Access(dirs, root); err != nil {
			return fail(err)
		}
	}
	// single-user auth is off by default; -auth, -token or $<NAME>_TOKEN turn it on
	tok := ""
	if token != nil {
		tok = *token
		if (*auth || *f.tunnel) && tok == "" { // a tunnel: anyone may find the address
			tok = myserver.NewToken()
		}
	}
	var tlsConf *tls.Config
	certSum, certMade := "", madeTLS{}
	var trustCert []byte
	if *f.https || *f.certFile != "" {
		if tlsConf, certSum, certMade, err = loadTLS(dirs, expandHome(*f.certFile), expandHome(*f.keyFile), *global); err != nil {
			return fail(fmt.Errorf("https: %w", err))
		}
		if *f.certFile == "" {
			trustCert, _ = os.ReadFile(caFile(dirs))
		}
	}

	con := console.New(console.Options{Name: name, NoTUI: *noTUI, NoOpen: *noOpen, Rows: rows})
	if *logFile != "" {
		if err := con.OpenLog(*logFile); err != nil {
			return fail(fmt.Errorf("log: %w", err))
		}
	}
	if app != nil {
		con.SetApp(app)
	}
	if createdData {
		con.Note("created your data folder: %s (give -d to serve another folder)", root)
	}
	instanceID := newInstanceID()
	var webVersion func() string
	var appAccess *myserver.AppAccess
	if app != nil {
		if !teamApp {
			webVersion = app.Current
		}
		if app.Current() != "" { // a first install says so once installed
			con.Note("%s may use: %s (more: %s app allow %s FOLDER)", app.Status().Name, folderList(app.Access()), name, app.Status().Name)
		}
		if spec.Access == nil { // an installed app: only its folders
			appAccess = limitApp(app, root)
		}
	}
	srv = myserver.New(myserver.Config{
		Root:      root,
		WebFS:     webFS,
		Manager:   managerFS,
		Port:      *port,
		Global:    *global,
		Public:    *f.tunnel,
		TLS:       tlsConf,
		TrustCert: trustCert,
		ReadOnly:  *readOnly,
		NoDAV:     *f.noDAV,
		TrustHTML: *f.trustHTML,
		CORS:      append(append([]string{}, myserver.DefaultCORS...), f.cors...),
		Token:     tok,
		Access:    access,
		OnRequest: con.OnRequest,
		// the flags use 0 for "off"; Config uses 0 for "default"
		KeepVersions: offIfZero(*versions),
		TrashDays:    offIfZero(*trashDays),
		MaxUpload:    *uploadMax << 20,
		Instance:     instanceID,
		WebVersion:   webVersion,
		App:          appAccess,
	})
	if len(f.cors) > 0 {
		con.AddRow("CORS", "+ "+f.cors.String())
	}
	switch {
	case *f.certFile != "":
		con.AddRow("HTTPS", tildePath(*f.certFile))
	case tlsConf != nil:
		trusted := trustedHere(dirs)
		if trusted {
			con.AddRow("HTTPS", "signed by this home's CA, which this machine trusts")
		} else {
			con.AddRow("HTTPS", "signed by this home's CA; SHA-256 "+fingerprint(certSum))
		}
		if *global {
			con.AddRowFunc("Trust it", func() string {
				u := srv.URL()
				if lan := srv.LANURLs(); len(lan) > 0 {
					u = lan[0]
				}
				return strings.TrimSuffix(u, "/") + myserver.CertPath + " on other devices"
			})
		}
		switch {
		case certMade.ca:
			con.Note("https: made a CA for this home's certificates (%s): %s tls trust makes this machine's browsers trust it; other devices open %s", tildePath(caFile(dirs)), name, myserver.CertPath)
		case !trusted:
			con.Note("https: browsers here warn until you run %s tls trust (or check the SHA-256 shown)", name)
		}
	}
	if *f.tunnel {
		con.AddRowFunc("Tunnel", func() string {
			if u := srv.PublicURL(); u != "" {
				return u
			}
			return "starting…"
		})
	}
	if srv.DAVURL() != "" && spec.Access == nil {
		con.AddRowFunc("WebDAV", func() string { // the port is known once started
			if tok != "" {
				return srv.DAVURL() + "  (password: the token)"
			}
			return srv.DAVURL()
		})
	}
	apps := &appSet{dirs: dirs, root: root, srv: srv, main: app, flags: *webFlags, ctx: context.Background(),
		on: *f.otherApps && spec.Access == nil}
	plugins.RegisterFileOps(srv)
	registerCatalogCmd(srv, spec, apps)
	registerAppCmd(srv, apps)
	registerAppsCmd(srv, spec, apps)
	if spec.Access == nil {
		con.SetCatalog(catalogUI{spec, apps})
	}
	if err := srv.Start(); err != nil {
		return fail(err)
	}
	if teamApp { // each person uses it with their own rights, within its folders
		var once sync.Once
		mountTeamApp = func() {
			once.Do(func() {
				port, err := srv.Mount(&myserver.Mount{Access: limitApp(app, root), FS: app.FS(), Version: app.Current}, app.Port())
				if err != nil {
					log.Printf("serving %s: %v", app.Status().Name, err)
					return
				}
				if app.Port() == 0 {
					app.SetPort(port)
				}
				log.Printf("%s is at /apps/%s/ (port %d); the file manager is at /", app.Status().Name, app.Status().Name, port)
				if apps.onChange != nil {
					go apps.onChange()
				}
			})
		}
		if app.Current() != "" {
			mountTeamApp()
		}
	}
	if apps.on {
		apps.mountAll()
	}
	for _, a := range srv.Mounted() {
		con.AddRow("app "+a.Name, a.URL)
	}
	if !*f.noUpgradeCheck {
		noticeUpgrade(name, dirs, log.Printf)
	}
	if *f.backupEvery > 0 {
		(&autoBackup{spec: spec, dirs: dirs, data: root, dir: backupsDirFor(dirs, *f.backupTo),
			every: time.Duration(*f.backupEvery), keep: max(*f.backupKeep, 1), logf: log.Printf}).start()
	}
	if app != nil {
		app.Start(context.Background())
	}
	if *f.tunnel {
		tun, err := startTunnel(srv.URL(), func(u string) {
			srv.SetPublicURL(u)
			if u != "" {
				log.Printf("tunnel: %s (sign in with the token: %s/?token=…)", u, u)
			}
		})
		if err != nil {
			srv.Shutdown(context.Background())
			return fail(err)
		}
		defer tun.stop()
	}
	if srv.Port() != *port {
		fmt.Fprintf(os.Stderr, "port %d is busy, using %d\n", *port, srv.Port())
	}
	self := &instance{ID: instanceID, PID: os.Getpid(), Port: srv.Port(), Root: root, Source: source,
		Version: myserver.VERSION, Args: os.Args[1:], Started: time.Now().UTC(), Cert: certSum}
	if srv.ServesRoot() {
		self.URL = srv.LoginURL()
	}
	if other != nil || *f.newCopy { // a -new copy leaves the main record to the first one
		runFile = strings.TrimSuffix(runFile, ".json") + "-" + instanceID[:8] + ".json"
	}
	self.Apps = apps.served()
	if err := self.record(runFile); err != nil {
		log.Printf("could not record the running server: %v", err)
	}
	apps.onChange = func() { // apps installed or removed while running
		self.Apps = apps.served()
		self.record(runFile)
	}
	defer self.forget(runFile)
	return con.Run(srv)
}

func offIfZero(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}
