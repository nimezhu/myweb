package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"myweb/myserver"
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

func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	c := exec.Command(cmd, args...)
	// keep the opener from scribbling over the TUI
	c.Stdout, c.Stderr = nil, nil
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait() // reap the child
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

var defaultCORS = []string{
	"https://codepen.io",
	"https://cdpn.io",
	"https://mycellome.com",
	"https://*.mycellome.com",
	"https://cellcycle.org",
	"https://*.cellcycle.org",
	"https://youdata.studio",
	"https://*.youdata.studio",
	"https://*.static.observableusercontent.com",
	"http://127.0.0.1:*",
	"https://127.0.0.1:*",
	"http://localhost:*",
	"https://localhost:*",
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
		fatal(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		fatal(fmt.Errorf("root directory %q does not exist or is not a directory", *directory))
	}

	var webFS http.FileSystem
	if len(*web) > 0 {
		if fi, err := os.Stat(*web); err != nil || !fi.IsDir() {
			fatal(fmt.Errorf("web directory %q does not exist or is not a directory", *web))
		}
		webFS = http.Dir(*web)
	} else {
		webFS = getFileSystem()
	}

	// Auth is off by default; -auth, -token or $MYWEB_TOKEN turn it on.
	if *auth && *token == "" {
		*token = myserver.NewToken()
	}

	useTUI := !*noTUI && isTerminal(os.Stdout) && isTerminal(os.Stdin)
	events := newBridge()

	m := http.NewServeMux()
	addWs(m) // TODO Improve Websocket with not only echo but services ...
	// TODO Add Services Plugins via srv.Mux()
	srv := myserver.New(myserver.Config{
		Root:     root,
		WebFS:    webFS,
		Port:     *port,
		Global:   *global,
		ReadOnly: *readOnly,
		CORS:     defaultCORS,
		Token:    *token,
		Mux:      m,
		OnRequest: func(ev myserver.RequestEvent) {
			if useTUI {
				events.send(reqMsg(ev))
			} else {
				log.Printf("%s %d %s %s %s", ev.Method, ev.Status, ev.Path, humanDuration(ev.Duration), ev.Remote)
			}
		},
	})
	registerPlugins(srv)
	if err := srv.Start(); err != nil {
		fatal(err)
	}
	if srv.Port() != *port {
		log.Printf("port %d is busy, using %d", *port, srv.Port())
	}
	if !*noOpen {
		if err := openBrowser(srv.LoginURL()); err != nil {
			log.Printf("could not open browser: %v", err)
		}
	}

	if useTUI {
		runTUI(srv, events)
	} else {
		runPlain(srv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "server shutdown error:", err)
		os.Exit(1)
	}
	fmt.Println("myweb stopped.")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "myweb:", err)
	os.Exit(1)
}

func runPlain(srv *myserver.Server) {
	log.Printf("myweb %s serving %s at %s", myserver.VERSION, srv.Root(), srv.URL())
	for _, u := range srv.LANURLs() {
		log.Printf("  also at %s", u)
	}
	if srv.AuthEnabled() {
		log.Printf("  token: %s", srv.Token())
		log.Printf("  open:  %s", srv.LoginURL())
	} else {
		log.Println("  auth off (enable with -auth)")
	}
	if srv.ReadOnly() {
		log.Println("Read only mode.")
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case s := <-stop:
		log.Printf("received %s, shutting down", s)
	case <-srv.StopRequested():
		log.Println("stop requested via /cmd/stopServer")
	case err := <-srv.Done():
		if err != nil {
			fatal(err)
		}
	}
}

func runTUI(srv *myserver.Server, events *bridge) {
	p := tea.NewProgram(newModel(srv), tea.WithAltScreen())

	// Route all log output into the TUI so it never corrupts the screen.
	log.SetFlags(0)
	log.SetOutput(events)
	defer log.SetOutput(os.Stderr)

	events.start(p)
	defer events.close()

	var serveErr error
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP)
		select {
		case <-stop:
		case <-srv.StopRequested():
		case serveErr = <-srv.Done():
		}
		p.Send(stopMsg{})
	}()

	if _, err := p.Run(); err != nil {
		fatal(fmt.Errorf("tui: %w", err))
	}
	if serveErr != nil {
		fatal(serveErr)
	}
}

// bridge decouples HTTP handlers and the logger from the TUI event loop: sends
// never block a request, and messages queued before the program starts are kept.
type bridge struct {
	ch      chan tea.Msg
	once    sync.Once
	done    chan struct{}
	mu      sync.Mutex
	partial string
}

func newBridge() *bridge {
	return &bridge{ch: make(chan tea.Msg, 4096), done: make(chan struct{})}
}

func (b *bridge) send(msg tea.Msg) {
	select {
	case b.ch <- msg:
	default: // UI is behind; drop rather than stall requests
	}
}

func (b *bridge) start(p *tea.Program) {
	go func() {
		for {
			select {
			case msg := <-b.ch:
				p.Send(msg)
			case <-b.done:
				return
			}
		}
	}()
}

func (b *bridge) close() { b.once.Do(func() { close(b.done) }) }

// Write implements io.Writer for the standard logger, one entry per line.
func (b *bridge) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.partial + string(data)
	lines := strings.Split(text, "\n")
	b.partial = lines[len(lines)-1]
	now := time.Now()
	for _, l := range lines[:len(lines)-1] {
		l = strings.ReplaceAll(l, "\t", "    ")
		if strings.TrimSpace(l) != "" {
			b.send(logMsg{t: now, text: l})
		}
	}
	return len(data), nil
}
