package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nimezhu/myweb/console"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/plugins"
)

//go:embed all:web
var embededFiles embed.FS

func getFileSystem() http.FileSystem {
	fsys, err := fs.Sub(embededFiles, "web")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}

func main() {
	port := flag.Int("p", 8433, "port to serve on (next free port is used if taken)")
	global := flag.Bool("global", false, "listen on all interfaces (visible to your network)")
	readOnly := flag.Bool("r", false, "read only (can be toggled in the TUI with 'r')")
	directory := flag.String("d", ".", "the root directory of data files")
	web := flag.String("web", "", "use static web directory (default: embedded)")
	noTUI := flag.Bool("no-tui", false, "plain log output instead of the interactive TUI")
	noOpen := flag.Bool("no-open", false, "do not open the browser on start")
	auth := flag.Bool("auth", false, "require an access token for writes and commands (random per start unless -token is given)")
	token := flag.String("token", os.Getenv("MYWEB_TOKEN"), "access token; setting it (or $MYWEB_TOKEN) turns on -auth")
	version := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(version, "v", false, "print version and exit (shorthand)")
	flag.Parse()

	if *version {
		fmt.Println("myweb", myserver.VERSION)
		return
	}

	root, err := filepath.Abs(*directory)
	if err != nil {
		console.Fatal("myweb", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		console.Fatal("myweb", fmt.Errorf("root directory %q does not exist or is not a directory", *directory))
	}

	var webFS http.FileSystem
	if len(*web) > 0 {
		if fi, err := os.Stat(*web); err != nil || !fi.IsDir() {
			console.Fatal("myweb", fmt.Errorf("web directory %q does not exist or is not a directory", *web))
		}
		webFS = http.Dir(*web)
	} else {
		webFS = getFileSystem()
	}

	// Auth is off by default; -auth, -token or $MYWEB_TOKEN turn it on.
	if *auth && *token == "" {
		*token = myserver.NewToken()
	}

	con := console.New(console.Options{Name: "myweb", NoTUI: *noTUI, NoOpen: *noOpen})
	srv := myserver.New(myserver.Config{
		Root:      root,
		WebFS:     webFS,
		Port:      *port,
		Global:    *global,
		ReadOnly:  *readOnly,
		CORS:      myserver.DefaultCORS,
		Token:     *token,
		OnRequest: con.OnRequest,
	})
	plugins.RegisterFileOps(srv)
	if err := srv.Start(); err != nil {
		console.Fatal("myweb", err)
	}
	if srv.Port() != *port {
		fmt.Fprintf(os.Stderr, "port %d is busy, using %d\n", *port, srv.Port())
	}
	os.Exit(con.Run(srv))
}
