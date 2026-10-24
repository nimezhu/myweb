package console

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

const maxEntries = 2000

// Messages fed into the TUI from the server and the log bridge.
type reqMsg myserver.RequestEvent
type logMsg struct {
	t    time.Time
	text string
}
type tickMsg time.Time
type stopMsg struct{} // a client asked the server to stop
type flashMsg int     // clears the transient status line if still current
type clipboardMsg struct{ err error }

// Ask the terminal to copy the URL, including when the server runs over SSH.
// OSC 52 has no acknowledgement: a successful write does not confirm a copy.
func copyURL(url string, out io.Writer) tea.Cmd {
	return func() tea.Msg {
		_, err := osc52.New(url).WriteTo(out)
		return clipboardMsg{err: err}
	}
}

type entry struct {
	id   int // stable identity: indexes shift as old entries are dropped
	req  *myserver.RequestEvent
	log  string
	time time.Time
}

type model struct {
	srv     *myserver.Server
	opts    Options
	started time.Time
	now     time.Time
	width   int
	height  int

	entries    []entry
	nextID     int
	errorsOnly bool
	filter     string
	typing     bool // editing the filter
	sel        int  // selected entry id; 0 = none, follow new requests
	top        int  // first visible row while an entry is selected
	detail     bool // show the selected entry in full
	qr         bool // show a QR code of the URL

	appView  bool              // the app view ('a')
	appSel   int               // selected row in it
	appAvail []webapp.Manifest // published versions, after 'p'
	appBusy  string            // a switch or listing in progress

	catView  bool          // the apps view ('i')
	catItems []CatalogItem // nil until listed
	catSel   int
	catBusy  string
	catErr   string

	requests int
	errors   int
	bytesOut int64

	flash    string
	flashID  int
	quitting bool
}

func newModel(srv *myserver.Server, opts Options) model {
	now := time.Now()
	return model{srv: srv, opts: opts, started: now, now: now, width: 80, height: 24}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m *model) push(e entry) {
	m.nextID++
	e.id = m.nextID
	m.entries = append(m.entries, e)
	if len(m.entries) > maxEntries {
		m.entries = append(m.entries[:0:0], m.entries[len(m.entries)-maxEntries:]...)
	}
	m.settle()
}

// visible applies the errors-only toggle and the text filter.
func (m model) visible(e entry) bool {
	if m.errorsOnly && e.req != nil && e.req.Status < 400 {
		return false
	}
	if m.filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(entryText(e)), strings.ToLower(m.filter))
}

func entryText(e entry) string {
	if e.req == nil {
		return e.log
	}
	return fmt.Sprintf("%s %d %s", e.req.Method, e.req.Status, e.req.Path)
}

func (m model) visibleEntries() []entry {
	var v []entry
	for _, e := range m.entries {
		if m.visible(e) {
			v = append(v, e)
		}
	}
	return v
}

func indexOf(v []entry, id int) int {
	for i, e := range v {
		if e.id == id {
			return i
		}
	}
	return -1
}

// settle keeps the selection valid and on screen.
func (m *model) settle() {
	if m.sel == 0 {
		m.detail = false
		return
	}
	v := m.visibleEntries()
	i := indexOf(v, m.sel)
	if i < 0 { // dropped or filtered out: back to following
		m.sel, m.detail = 0, false
		return
	}
	h := m.logHeight()
	if i < m.top {
		m.top = i
	}
	if i >= m.top+h {
		m.top = i - h + 1
	}
	m.top = min(max(m.top, 0), max(0, len(v)-h))
}

// move shifts the selection by delta rows; past the end returns to following.
func (m *model) move(delta int) {
	v := m.visibleEntries()
	if len(v) == 0 {
		return
	}
	i := indexOf(v, m.sel)
	if i < 0 {
		if delta > 0 {
			return // already following
		}
		i = len(v) // start from just below the last row
		m.top = max(0, len(v)-m.logHeight())
	}
	i += delta
	if i >= len(v) {
		m.sel = 0
		m.settle()
		return
	}
	m.sel = v[max(i, 0)].id
	m.settle()
}

func (m *model) setFlash(s string) tea.Cmd {
	m.flash = s
	m.flashID++
	id := m.flashID
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashMsg(id) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.settle()
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case flashMsg:
		if int(msg) == m.flashID {
			m.flash = ""
		}
	case clipboardMsg:
		if msg.err != nil {
			return m, m.setFlash("could not copy URL: " + msg.err.Error())
		}
		return m, m.setFlash("URL sent to terminal clipboard")
	case reqMsg:
		ev := myserver.RequestEvent(msg)
		m.requests++
		m.bytesOut += ev.Bytes
		if ev.Status >= 400 {
			m.errors++
		}
		m.push(entry{req: &ev, time: ev.Time})
	case logMsg:
		m.push(entry{log: msg.text, time: msg.t})
	case appDoneMsg:
		m.appBusy = ""
		return m, m.setFlash(msg.flash)
	case catListMsg:
		m.catBusy, m.catErr = "", ""
		if msg.err != nil {
			m.catErr = msg.err.Error()
		}
		if msg.items != nil || m.catItems == nil {
			m.catItems = msg.items
			if m.catItems == nil {
				m.catItems = []CatalogItem{}
			}
		}
		m.catSel = min(m.catSel, max(0, len(m.catItems)-1))
	case catDoneMsg:
		m.catBusy = ""
		if msg.open != "" {
			if err := openURL(msg.open); err != nil {
				return m, m.setFlash("could not open browser: " + err.Error())
			}
		}
		return m, tea.Batch(m.setFlash(msg.flash), m.catLoad(false))
	case appAvailMsg:
		m.appBusy = ""
		if msg.err != nil {
			return m, m.setFlash("could not list published versions: " + msg.err.Error())
		}
		m.appAvail, m.appSel = msg.list, 0
	case stopMsg:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.typing {
			return m.filterKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.typing = false
	case tea.KeyEsc:
		m.typing, m.filter = false, ""
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
	}
	m.settle()
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.catView {
		if mm, cmd, done := m.catKey(msg); done {
			return mm, cmd
		}
	}
	if m.appView {
		if mm, cmd, done := m.appKey(msg); done {
			return mm, cmd
		}
	}
	page := max(1, m.logHeight()-1)
	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "esc": // step back out: detail → selection → filter
		switch {
		case m.qr:
			m.qr = false
		case m.detail:
			m.detail = false
		case m.sel != 0:
			m.sel = 0
		case m.filter != "" || m.errorsOnly:
			m.filter, m.errorsOnly = "", false
		}
	case "m":
		u := m.srv.ManagerLoginURL()
		if u == "" {
			return m, m.setFlash("the file manager is off (-no-manager)")
		}
		if err := OpenBrowser(u); err != nil {
			return m, m.setFlash("could not open browser: " + err.Error())
		}
		return m, m.setFlash("opened the file manager")
	case "i":
		if m.opts.Catalog == nil {
			return m, m.setFlash("no app catalogs here")
		}
		m.catView, m.appView, m.qr, m.detail = true, false, false, false
		return m, m.catLoad(false)
	case "a":
		if m.opts.App == nil {
			return m, m.setFlash("no auto-updated app (start with -web-from or -app)")
		}
		if _, ok := m.appVersions(); ok {
			m.appView, m.qr, m.detail, m.appSel = true, false, false, 0
			return m, nil
		}
		m.opts.App.CheckNow()
		return m, m.setFlash("checking for an app update…")
	case "o":
		if m.opts.OpenURL == "" {
			return m, m.setFlash("nothing is served at / (no -web, and -no-manager)")
		}
		if err := OpenBrowser(m.opts.OpenURL); err != nil {
			return m, m.setFlash("could not open browser: " + err.Error())
		}
		return m, m.setFlash("opened " + m.opts.OpenURL)
	case "y":
		if m.opts.OpenURL == "" {
			return m, m.setFlash("nothing is served at / (no -web, and -no-manager)")
		}
		return m, copyURL(m.opts.OpenURL, os.Stdout)
	case "r":
		m.srv.SetReadOnly(!m.srv.ReadOnly())
		if m.srv.ReadOnly() {
			return m, m.setFlash("now read-only: write, remove and upload are disabled")
		}
		return m, m.setFlash("now read-write")
	case "u":
		m.qr = !m.qr
		m.detail, m.appView = false, false
	case "/":
		m.typing, m.qr = true, false
	case "e":
		m.errorsOnly = !m.errorsOnly
	case "c":
		m.entries = nil
		m.sel, m.detail = 0, false
		m.requests, m.errors, m.bytesOut = 0, 0, 0
		return m, m.setFlash("log cleared")
	case "enter":
		if m.sel != 0 {
			m.detail = !m.detail
			m.qr = false
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-page)
	case "pgdown", "ctrl+d", " ":
		m.move(page)
	case "home", "g":
		if v := m.visibleEntries(); len(v) > 0 {
			m.sel, m.top = v[0].id, 0
		}
	case "end", "G", "f":
		m.sel = 0
	}
	m.settle()
	return m, nil
}

// ---- styles ----

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A3FD8", Dark: "#A08CFF"}
	subtle    = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	textColor = lipgloss.AdaptiveColor{Light: "#222222", Dark: "#E4E4E4"}
	green     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#56D364"}
	yellow    = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	red       = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	cyan      = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#79C0FF"}

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(accent).Padding(0, 1)
	labelStyle  = lipgloss.NewStyle().Foreground(subtle).Width(9)
	valueStyle  = lipgloss.NewStyle().Foreground(textColor)
	urlStyle    = lipgloss.NewStyle().Foreground(cyan).Underline(true)
	dimStyle    = lipgloss.NewStyle().Foreground(subtle)
	keyStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	ruleStyle   = lipgloss.NewStyle().Foreground(subtle)
	flashStyle  = lipgloss.NewStyle().Foreground(yellow)
	logStyle    = lipgloss.NewStyle().Foreground(subtle).Italic(true)
	methodStyle = lipgloss.NewStyle().Bold(true).Width(7)
)

func badge(text string, c lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(text)
}

func statusStyle(code int) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	switch {
	case code >= 500:
		return s.Foreground(red)
	case code >= 400:
		return s.Foreground(yellow)
	case code >= 300:
		return s.Foreground(cyan)
	default:
		return s.Foreground(green)
	}
}

// ---- view ----

const headerLines = 2 // title + rule

func (m model) infoLines() []string {
	row := func(label, value string) string {
		return " " + labelStyle.Render(label) + value
	}
	lines := []string{
		row("URL", urlStyle.Render(m.srv.URL())+dimStyle.Render("  (o open, y copy)")),
	}
	for _, u := range m.srv.LANURLs() {
		lines = append(lines, row("LAN", urlStyle.Render(u)))
	}
	if u := m.srv.ManagerURL(); u != "" && !m.srv.ManagerAtRoot() {
		lines = append(lines, row("Files", urlStyle.Render(u)+dimStyle.Render("  file manager (m)")))
	}
	if m.opts.App != nil {
		hint := "  (a: check now)"
		if _, ok := m.appVersions(); ok {
			hint = "  (a: versions)"
		}
		lines = append(lines, row("App", valueStyle.Render(m.opts.App.StatusLine())+dimStyle.Render(hint)))
	}
	for _, r := range m.opts.Rows {
		val := r.Value
		if r.Func != nil {
			val = r.Func()
		}
		v := valueStyle.Render(val)
		if strings.Contains(val, "://") {
			v = urlStyle.Render(val)
		}
		lines = append(lines, row(r.Label, v))
	}
	lines = append(lines, row("Root", valueStyle.Render(m.srv.Root())))
	if m.srv.MultiUser() {
		lines = append(lines, row("Sign-in", valueStyle.Render("personal links")+
			dimStyle.Render("  (myweb-team user add NAME)")))
	} else if m.srv.AuthEnabled() {
		lines = append(lines, row("Token", valueStyle.Render(m.srv.Token())+
			dimStyle.Render("  send as Authorization: Bearer <token>")))
	} else {
		lines = append(lines, row("Token", dimStyle.Render("off (enable with -auth)")))
	}

	mode := badge("read-write", green)
	if m.srv.ReadOnly() {
		mode = badge("read-only", yellow)
	}
	access := dimStyle.Render("local only")
	if m.srv.Global() {
		access = badge("network-visible", yellow)
	}
	sep := dimStyle.Render("  │  ")
	safety := dimStyle.Render("no history")
	versions, trash := m.srv.Safety()
	if versions > 0 {
		safety = valueStyle.Render(fmt.Sprintf("%d versions", versions))
	}
	if trash > 0 {
		safety += dimStyle.Render(" · ") + valueStyle.Render(fmt.Sprintf("trash %dd", trash))
	} else {
		safety += dimStyle.Render(" · ") + badge("deletes are permanent", yellow)
	}
	lines = append(lines, row("Mode", mode+sep+access+sep+safety))

	errs := dimStyle.Render("0 errors")
	if m.errors > 0 {
		errs = badge(fmt.Sprintf("%d errors", m.errors), red)
	}
	lines = append(lines, row("Stats",
		valueStyle.Render(fmt.Sprintf("%d requests", m.requests))+sep+errs+sep+
			valueStyle.Render(humanBytes(m.bytesOut)+" sent")+m.liveClients(sep)))

	if m.srv.Global() && !m.srv.ReadOnly() && !m.srv.AuthEnabled() {
		lines = append(lines, " "+badge("⚠ anyone on your network can modify files in Root (press r for read-only)", yellow))
	}
	if m.srv.Global() && m.srv.MultiUser() {
		lines = append(lines, " "+badge("⚠ plain HTTP: sign-in links can be seen on the network; put HTTPS in front (see README)", yellow))
	}
	return lines
}

func (m model) footerLines() []string {
	type key struct{ k, d string }
	var keys []key
	switch {
	case m.typing:
		keys = []key{{"⏎", "apply"}, {"esc", "clear"}}
	case m.catView:
		keys = []key{{"↑↓", "select"}, {"⏎", "install / open"}, {"r", "refresh"}, {"esc", "back"}, {"q", "quit"}}
	case m.appView:
		keys = []key{{"↑↓", "select"}, {"⏎", "use version"}, {"l", "follow updates"}, {"c", "check now"},
			{"p", "all published"}, {"esc", "back"}, {"q", "quit"}}
	case m.detail:
		keys = []key{{"↑↓", "prev/next"}, {"y", "copy URL"}, {"esc", "back"}, {"q", "quit"}}
	case m.qr:
		keys = []key{{"u", "close"}, {"o", "open"}, {"y", "copy URL"}, {"q", "quit"}}
	default:
		keys = []key{{"o", "open"}, {"y", "copy URL"}}
		if m.srv.ManagerURL() != "" && !m.srv.ManagerAtRoot() {
			keys = append(keys, key{"m", "files"})
		}
		if m.opts.Catalog != nil {
			keys = append(keys, key{"i", "apps"})
		}
		keys = append(keys, []key{
			{"r", "read-only"}, {"/", "filter"}, {"e", "errors"},
			{"↑↓", "select"}, {"⏎", "details"}, {"u", "qr"}, {"c", "clear"}, {"q", "quit"},
		}...)
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, keyStyle.Render(k.k)+" "+dimStyle.Render(k.d))
	}
	help := " " + strings.Join(parts, dimStyle.Render(" · "))
	status := " "
	switch {
	case m.typing:
		status = " " + keyStyle.Render("/") + " " + valueStyle.Render(m.filter) + keyStyle.Render("█")
	case m.flash != "":
		status = " " + flashStyle.Render(m.flash)
	}
	return []string{status, help}
}

func (m model) logHeight() int {
	used := headerLines + len(m.infoLines()) + 1 /* section rule */ + len(m.footerLines()) + 1 /* bottom rule */
	return max(1, m.height-used)
}

func (m model) rule(title string) string {
	if title == "" {
		return ruleStyle.Render(strings.Repeat("─", max(0, m.width)))
	}
	head := "── " + title + " "
	return ruleStyle.Render(head + strings.Repeat("─", max(0, m.width-lipgloss.Width(head))))
}

func (m model) renderEntry(e entry) string {
	ts := dimStyle.Render(e.time.Format("15:04:05"))
	if e.req == nil {
		return ts + "  " + logStyle.Render(strings.TrimRight(e.log, "\n"))
	}
	r := e.req
	return fmt.Sprintf("%s  %s %s %s %s  %s",
		ts,
		methodStyle.Render(r.Method),
		statusStyle(r.Status).Render(fmt.Sprintf("%3d", r.Status)),
		dimStyle.Render(fmt.Sprintf("%8s", humanDuration(r.Duration))),
		dimStyle.Render(fmt.Sprintf("%8s", humanBytes(r.Bytes))),
		userTag(r.User)+valueStyle.Render(r.Path),
	)
}

// userTag shows who made a request (multi-user only).
func userTag(u string) string {
	if u == "" {
		return ""
	}
	return keyStyle.Render(u) + " "
}

// listLines renders the request log, following the newest entries unless
// one is selected.
func (m model) listLines(v []entry, h int) (lines []string, newer int) {
	if len(v) == 0 {
		msg := "  waiting for requests…"
		if m.filter != "" || m.errorsOnly {
			msg = "  nothing matches (esc clears the filter)"
		}
		return []string{dimStyle.Render(msg)}, 0
	}
	start, end := max(0, len(v)-h), len(v)
	if m.sel != 0 {
		start = m.top
		end = min(len(v), start+h)
	}
	for _, e := range v[start:end] {
		marker := " "
		if e.id == m.sel {
			marker = keyStyle.Render("›")
		}
		lines = append(lines, marker+m.renderEntry(e))
	}
	return lines, len(v) - end
}

// detailLines shows one entry in full, wrapped to the width.
func (m model) detailLines(e entry) []string {
	w := max(20, m.width-12)
	wrap := lipgloss.NewStyle().Width(w)
	row := func(label, value string) []string {
		if value == "" {
			return nil
		}
		parts := strings.Split(wrap.Render(value), "\n")
		out := []string{" " + lipgloss.NewStyle().Foreground(subtle).Width(10).Render(label) + parts[0]}
		for _, p := range parts[1:] {
			out = append(out, strings.Repeat(" ", 11)+p)
		}
		return out
	}
	var out []string
	add := func(l []string) { out = append(out, l...) }
	add(row("Time", e.time.Format("2006-01-02 15:04:05.000")))
	if e.req == nil {
		add(row("Log", e.log))
		return out
	}
	r := e.req
	add(row("Request", r.Method+" "+r.Path))
	add([]string{" " + lipgloss.NewStyle().Foreground(subtle).Width(10).Render("Status") +
		statusStyle(r.Status).Render(fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status)))})
	add(row("Duration", humanDuration(r.Duration)))
	add(row("Size", humanBytes(r.Bytes)))
	add(row("User", r.User))
	add(row("Client", r.Remote))
	add(row("Origin", r.Origin))
	add(row("Agent", r.UserAgent))
	add(row("Response", strings.TrimSpace(r.Error)))
	return out
}

// qrLines draws the URL a phone would use as a QR code (two rows per line).
func (m model) qrLines(h int) []string {
	target, note := m.srv.URL(), "only reachable from this machine; start with -global to use it from a phone"
	if lan := m.srv.LANURLs(); len(lan) > 0 {
		target, note = lan[0], "scan to open on another device on this network"
	}
	if m.srv.AuthEnabled() {
		target += "/?token=" + m.srv.Token()
		note += " (signs it in)"
	}
	q, err := qrcode.New(target, qrcode.Low)
	if err != nil {
		return []string{dimStyle.Render("  cannot make a QR code: " + err.Error())}
	}
	q.DisableBorder = false
	bits := q.Bitmap()
	if (len(bits)+1)/2+2 > h || len(bits)+2 > m.width {
		return []string{dimStyle.Render("  make the terminal larger to show the QR code"), "  " + urlStyle.Render(target)}
	}
	// explicit black-on-white, so it scans in dark and light terminals alike
	cell := func(top, bottom bool) string {
		fg, bg := "231", "231"
		if top {
			fg = "16"
		}
		if bottom {
			bg = "16"
		}
		return "\x1b[38;5;" + fg + ";48;5;" + bg + "m▀"
	}
	var out []string
	for y := 0; y < len(bits); y += 2 {
		var b strings.Builder
		b.WriteString("  ")
		for x := range bits[y] {
			bottom := y+1 < len(bits) && bits[y+1][x]
			b.WriteString(cell(bits[y][x], bottom))
		}
		b.WriteString("\x1b[0m")
		out = append(out, b.String())
	}
	return append(out, "  "+urlStyle.Render(target), "  "+dimStyle.Render(note))
}

func (m model) View() string {
	if m.quitting {
		return dimStyle.Render(" stopping server…") + "\n"
	}
	w := max(20, m.width)
	clip := lipgloss.NewStyle().MaxWidth(w)

	var b strings.Builder
	line := func(s string) {
		b.WriteString(clip.Render(s))
		b.WriteByte('\n')
	}

	// title bar
	uptime := m.now.Sub(m.started).Truncate(time.Second)
	left := titleStyle.Render(m.opts.Name) + " " + dimStyle.Render("v"+m.opts.Version)
	right := badge("● running", green) + "  " + dimStyle.Render("up "+uptime.String()) + " "
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	line(left + strings.Repeat(" ", gap) + right)
	line(m.rule(""))

	for _, l := range m.infoLines() {
		line(l)
	}

	h := m.logHeight()
	v := m.visibleEntries()
	var title string
	var body []string
	switch {
	case m.catView:
		title, body = "Apps", m.catLines(h)
	case m.appView:
		title, body = "App", m.appLines(h)
	case m.qr:
		title, body = "URL", m.qrLines(h)
	case m.detail && indexOf(v, m.sel) >= 0:
		title, body = "Request", m.detailLines(v[indexOf(v, m.sel)])
	default:
		var newer int
		body, newer = m.listLines(v, h)
		title = "Requests"
		if m.errorsOnly {
			title += " · errors only"
		}
		if m.filter != "" {
			title += fmt.Sprintf(" · filter %q", m.filter)
		}
		if newer > 0 {
			title += fmt.Sprintf(" · paused, %d newer below", newer)
		}
	}
	line(m.rule(title))
	if len(body) > h {
		body = body[:h]
	}
	for _, l := range body {
		line(l)
	}
	for i := len(body); i < h; i++ {
		b.WriteByte('\n')
	}

	line(m.rule(""))
	footer := m.footerLines()
	for i, l := range footer {
		if i == len(footer)-1 {
			b.WriteString(clip.Render(l)) // no trailing newline: avoids scrolling the alt screen
		} else {
			line(l)
		}
	}
	return b.String()
}

// ---- helpers ----

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

func (m model) liveClients(sep string) string {
	n := m.srv.EventClients()
	if n == 0 {
		return ""
	}
	return sep + badge(fmt.Sprintf("%d live", n), cyan)
}
