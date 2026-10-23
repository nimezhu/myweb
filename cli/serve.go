// Package cli is the command line of a myweb server: flags, folders, the
// console, and starting and stopping the server. myweb and myweb-team are
// thin wrappers around Serve.
package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
}

// Serve runs the server until it is stopped and returns the exit code.
func Serve(spec Spec) int {
	name := spec.Name
	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	fail := func(err error) int {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 1
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	port := fs.Int("p", 8433, "port to serve on (next free port is used if taken)")
	global := fs.Bool("global", false, "listen on all interfaces (visible to your network)")
	readOnly := fs.Bool("r", false, "read only (can be toggled in the TUI with 'r')")
	homeDir := fs.String("home", "", fmt.Sprintf("%s's own folder, holding data/ and apps/ (default $%s_HOME, or ~/.%s)", name, env, name))
	directory := fs.String("d", "", "the folder of data files to serve (default <home>/data;\n-d . for the current folder)")
	web := fs.String("web", "", "serve this web app at / (the file manager stays at "+myserver.ManagerPath+")")
	noManager := fs.Bool("no-manager", false, "don't serve the built-in file manager")
	noTUI := fs.Bool("no-tui", false, "plain log output instead of the interactive TUI")
	noOpen := fs.Bool("no-open", false, "do not open the browser on start")
	var auth *bool
	var token *string
	if spec.Access == nil {
		auth = fs.Bool("auth", false, "require an access token for writes and commands (random per start unless -token is given)")
		token = fs.String("token", os.Getenv(env+"_TOKEN"), fmt.Sprintf("access token; setting it (or $%s_TOKEN) turns on -auth", env))
	}
	versions := fs.Int("versions", 20, "previous versions kept per file on overwrite (0: off)")
	trashDays := fs.Int("trash-days", 30, "days deleted files stay in the trash (0: delete immediately)")
	uploadMax := fs.Int64("upload-max", 100, "largest file accepted by an upload, in MiB")
	logFile := fs.String("log", "", "also append requests and messages to this file (JSON lines)")
	var cors myserver.OriginList
	fs.Var(&cors, "cors", fmt.Sprintf("extra browser origins allowed to call the API, comma-separated or repeated\n(e.g. https://app.example.com); also $%s_CORS", env))
	version := fs.Bool("version", false, "print version and exit")
	fs.BoolVar(version, "v", false, "print version and exit (shorthand)")
	webFlags := webapp.AddFlags(fs)
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
	if v := os.Getenv(env + "_CORS"); v != "" {
		if err := cors.Set(v); err != nil {
			return fail(fmt.Errorf("%s_CORS: %w", env, err))
		}
	}
	if *web != "" && webFlags.From != "" {
		return fail(fmt.Errorf("use -web or -web-from, not both"))
	}

	dirs, err := home.For(name)
	if *homeDir != "" {
		dirs.Root, err = filepath.Abs(*homeDir)
	}
	if err != nil {
		return fail(err)
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
	app, err := webFlags.Installer(myserver.VERSION, dirs.Apps(), func(n, v string) {
		srv.Broadcast(map[string]string{"type": "app-updated", "name": n, "version": v})
	})
	if err != nil {
		return fail(err)
	}
	if app != nil {
		webFS = app.FS()
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
		if *auth && tok == "" {
			tok = myserver.NewToken()
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
	srv = myserver.New(myserver.Config{
		Root:      root,
		WebFS:     webFS,
		Manager:   managerFS,
		Port:      *port,
		Global:    *global,
		ReadOnly:  *readOnly,
		CORS:      append(append([]string{}, myserver.DefaultCORS...), cors...),
		Token:     tok,
		Access:    access,
		OnRequest: con.OnRequest,
		// the flags use 0 for "off"; Config uses 0 for "default"
		KeepVersions: offIfZero(*versions),
		TrashDays:    offIfZero(*trashDays),
		MaxUpload:    *uploadMax << 20,
	})
	if len(cors) > 0 {
		con.AddRow("CORS", "+ "+cors.String())
	}
	plugins.RegisterFileOps(srv)
	if err := srv.Start(); err != nil {
		return fail(err)
	}
	if app != nil {
		app.Start(context.Background())
	}
	if srv.Port() != *port {
		fmt.Fprintf(os.Stderr, "port %d is busy, using %d\n", *port, srv.Port())
	}
	return con.Run(srv)
}

func offIfZero(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}
