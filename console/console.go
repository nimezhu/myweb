// Package console runs a myserver.Server in the terminal: an interactive TUI
// when attached to a terminal, plain log lines otherwise.
//
//	con := console.New(console.Options{Name: "myweb"})
//	srv := myserver.New(myserver.Config{..., OnRequest: con.OnRequest})
//	if err := srv.Start(); err != nil { console.Fatal("myweb", err) }
//	os.Exit(con.Run(srv))
package console

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nimezhu/myweb/myserver"
)

// Row is an extra line in the TUI's info panel (and the plain-mode banner).
type Row struct {
	Label string
	Value string
}

// Options configures the console.
type Options struct {
	Name    string // app name in the title bar and messages
	Version string // default: myserver.VERSION
	NoTUI   bool   // force plain log output
	NoOpen  bool   // don't open the browser on start
	// OpenURL is opened on start and by the 'o' key.
	// Default: the server's LoginURL (signs the browser in when auth is on).
	OpenURL string
	Rows    []Row
	// App, if set, is the installed web app (-web-from): shown in the TUI,
	// where 'a' checks for an update.
	App App
}

// App is what the console shows about an auto-updated web app.
type App interface {
	StatusLine() string
	CheckNow()
}

// SetApp attaches an installed web app (call before Run).
func (c *Console) SetApp(a App) { c.opts.App = a }

// Console owns terminal output for one server.
type Console struct {
	opts   Options
	useTUI bool
	events *bridge
	sig    chan os.Signal
	alog   *accessLog
	stderr *log.Logger // request lines in plain mode (not in the -log file twice)
}

// New decides between TUI and plain mode. Create it before the server so
// its OnRequest can be passed in myserver.Config.
func New(opts Options) *Console {
	if opts.Name == "" {
		opts.Name = "myweb"
	}
	if opts.Version == "" {
		opts.Version = myserver.VERSION
	}
	c := &Console{
		opts:   opts,
		useTUI: !opts.NoTUI && isTerminal(os.Stdout) && isTerminal(os.Stdin),
		events: newBridge(),
		sig:    make(chan os.Signal, 1),
		stderr: log.New(os.Stderr, "", log.LstdFlags),
	}
	// Listen from the start: a background process inherits SIGINT as ignored,
	// so a signal arriving before Run would otherwise be lost.
	signal.Notify(c.sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	return c
}

// SetOpenURL changes what is opened on start and by 'o' (call before Run);
// useful when the URL depends on the server's port.
func (c *Console) SetOpenURL(url string) { c.opts.OpenURL = url }

// AddRow appends a line to the info panel (call before Run).
func (c *Console) AddRow(label, value string) {
	c.opts.Rows = append(c.opts.Rows, Row{Label: label, Value: value})
}

// OpenLog also appends requests and server messages to path, one JSON object
// per line (see accesslog.go). Call before Run.
func (c *Console) OpenLog(path string) error {
	a, err := openAccessLog(path)
	if err != nil {
		return err
	}
	c.alog = a
	c.AddRow("Log", path)
	return nil
}

// OnRequest feeds the request log; use it as myserver.Config.OnRequest.
func (c *Console) OnRequest(ev myserver.RequestEvent) {
	if c.alog != nil {
		c.alog.request(ev)
	}
	if c.useTUI {
		c.events.send(reqMsg(ev))
	} else {
		c.stderr.Printf("%s %d %s %s %s", ev.Method, ev.Status, ev.Path, humanDuration(ev.Duration), ev.Remote)
	}
}

// logOutput routes the standard logger to w, and to the -log file if any.
func (c *Console) logOutput(w io.Writer) {
	if c.alog != nil {
		w = io.MultiWriter(w, &lineWriter{fn: c.alog.message})
	}
	log.SetOutput(w)
}

// Run opens the browser (unless NoOpen), shows the console until the user
// quits, a signal arrives, /cmd/stopServer is called or serving fails, then
// shuts the server down. It returns the process exit code.
func (c *Console) Run(srv *myserver.Server) int {
	if c.opts.OpenURL == "" && srv.ServesRoot() {
		c.opts.OpenURL = srv.LoginURL()
	}
	if !c.opts.NoOpen && c.opts.OpenURL != "" {
		if err := OpenBrowser(c.opts.OpenURL); err != nil {
			log.Printf("could not open browser: %v", err)
		}
	}

	defer signal.Stop(c.sig)
	if c.alog != nil {
		defer c.alog.close()
	}
	var err error
	if c.useTUI {
		err = c.runTUI(srv)
	} else {
		err = c.runPlain(srv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := srv.Shutdown(ctx); serr != nil && err == nil {
		err = fmt.Errorf("shutdown: %w", serr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", c.opts.Name, err)
		return 1
	}
	fmt.Printf("%s stopped.\n", c.opts.Name)
	return 0
}

// Fatal prints "name: err" and exits with status 1.
func Fatal(name string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	os.Exit(1)
}

func (c *Console) runPlain(srv *myserver.Server) error {
	c.logOutput(os.Stderr)
	defer log.SetOutput(os.Stderr)
	log.Printf("%s %s serving %s at %s", c.opts.Name, c.opts.Version, srv.Root(), srv.URL())
	for _, u := range srv.LANURLs() {
		log.Printf("  also at %s", u)
	}
	for _, r := range c.opts.Rows {
		log.Printf("  %s: %s", strings.ToLower(r.Label), r.Value)
	}
	if c.opts.App != nil {
		log.Printf("  app:   %s", c.opts.App.StatusLine())
	}
	if srv.AuthEnabled() {
		log.Printf("  token: %s", srv.Token())
	} else {
		log.Println("  auth off (enable with -auth)")
	}
	if c.opts.OpenURL != "" {
		log.Printf("  open:  %s", c.opts.OpenURL)
	}
	if u := srv.ManagerURL(); u != "" && !srv.ManagerAtRoot() {
		log.Printf("  files: %s", u)
	}
	if srv.ReadOnly() {
		log.Println("Read only mode.")
	}
	select {
	case s := <-c.sig:
		log.Printf("received %s, shutting down", s)
	case <-srv.StopRequested():
		log.Println("stop requested via /cmd/stopServer")
	case err := <-srv.Done():
		return err
	}
	return nil
}

func (c *Console) runTUI(srv *myserver.Server) error {
	p := tea.NewProgram(newModel(srv, c.opts), tea.WithAltScreen())

	// Route all log output into the TUI so it never corrupts the screen.
	flags := log.Flags()
	log.SetFlags(0)
	c.logOutput(c.events)
	defer func() { log.SetOutput(os.Stderr); log.SetFlags(flags) }()

	c.events.start(p)
	defer c.events.close()

	var serveErr error
	done := make(chan struct{})
	go func() {
		select {
		case <-c.sig:
		case <-srv.StopRequested():
		case serveErr = <-srv.Done():
		case <-done:
			return
		}
		p.Send(stopMsg{})
	}()

	_, err := p.Run()
	close(done)
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return serveErr
}

// OpenBrowser opens url with the platform's default handler.
func OpenBrowser(url string) error {
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
