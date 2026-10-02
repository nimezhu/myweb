package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/manager"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/plugins"
)

func main() {
	port := flag.Int("p", 8433, "port to serve on (next free port is used if taken)")
	global := flag.Bool("global", false, "listen on all interfaces (visible to your network)")
	readOnly := flag.Bool("r", false, "read only (can be toggled in the TUI with 'r')")
	directory := flag.String("d", ".", "the root directory of data files")
	web := flag.String("web", "", "serve this web app at / (the file manager stays at "+myserver.ManagerPath+")")
	noManager := flag.Bool("no-manager", false, "don't serve the built-in file manager")
	noTUI := flag.Bool("no-tui", false, "plain log output instead of the interactive TUI")
	noOpen := flag.Bool("no-open", false, "do not open the browser on start")
	auth := flag.Bool("auth", false, "require an access token for writes and commands (random per start unless -token is given)")
	token := flag.String("token", os.Getenv("MYWEB_TOKEN"), "access token; setting it (or $MYWEB_TOKEN) turns on -auth")
	versions := flag.Int("versions", 20, "previous versions kept per file on overwrite (0: off)")
	trashDays := flag.Int("trash-days", 30, "days deleted files stay in the trash (0: delete immediately)")
	uploadMax := flag.Int64("upload-max", 100, "largest file accepted by an upload, in MiB")
	logFile := flag.String("log", "", "also append requests and messages to this file (JSON lines)")
	var cors myserver.OriginList
	flag.Var(&cors, "cors", "extra browser origins allowed to call the API, comma-separated or repeated\n(e.g. https://app.example.com); also $MYWEB_CORS")
	version := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(version, "v", false, "print version and exit (shorthand)")
	flag.Parse()

	if *version {
		fmt.Println("myweb", myserver.VERSION)
		return
	}

	if env := os.Getenv("MYWEB_CORS"); env != "" {
		if err := cors.Set(env); err != nil {
			console.Fatal("myweb", fmt.Errorf("MYWEB_CORS: %w", err))
		}
	}

	root, err := filepath.Abs(*directory)
	if err != nil {
		console.Fatal("myweb", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		console.Fatal("myweb", fmt.Errorf("root directory %q does not exist or is not a directory", *directory))
	}

	var webFS, managerFS http.FileSystem
	if len(*web) > 0 {
		if fi, err := os.Stat(*web); err != nil || !fi.IsDir() {
			console.Fatal("myweb", fmt.Errorf("web directory %q does not exist or is not a directory", *web))
		}
		webFS = http.Dir(*web)
	}
	if !*noManager {
		managerFS = manager.FS()
	}

	// Auth is off by default; -auth, -token or $MYWEB_TOKEN turn it on.
	if *auth && *token == "" {
		*token = myserver.NewToken()
	}

	con := console.New(console.Options{Name: "myweb", NoTUI: *noTUI, NoOpen: *noOpen})
	if *logFile != "" {
		if err := con.OpenLog(*logFile); err != nil {
			console.Fatal("myweb", fmt.Errorf("log: %w", err))
		}
	}
	srv := myserver.New(myserver.Config{
		Root:      root,
		WebFS:     webFS,
		Manager:   managerFS,
		Port:      *port,
		Global:    *global,
		ReadOnly:  *readOnly,
		CORS:      append(append([]string{}, myserver.DefaultCORS...), cors...),
		Token:     *token,
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
		console.Fatal("myweb", err)
	}
	if srv.Port() != *port {
		fmt.Fprintf(os.Stderr, "port %d is busy, using %d\n", *port, srv.Port())
	}
	os.Exit(con.Run(srv))
}

func offIfZero(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}
