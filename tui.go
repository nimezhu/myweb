package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"myweb/myserver"
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

type entry struct {
	req  *myserver.RequestEvent
	log  string
	time time.Time
}

type model struct {
	srv     *myserver.Server
	started time.Time
	now     time.Time
	width   int
	height  int

	entries    []entry
	scroll     int // lines scrolled up from the bottom; 0 = follow
	errorsOnly bool

	requests int
	errors   int
	bytesOut int64

	flash    string
	flashID  int
	quitting bool
}

func newModel(srv *myserver.Server) model {
	now := time.Now()
	return model{srv: srv, started: now, now: now, width: 80, height: 24}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m *model) push(e entry) {
	m.entries = append(m.entries, e)
	if len(m.entries) > maxEntries {
		m.entries = append(m.entries[:0:0], m.entries[len(m.entries)-maxEntries:]...)
	}
	// keep the view anchored when the user has scrolled up
	if m.scroll > 0 && m.visible(e) {
		m.scroll++
	}
}

func (m model) visible(e entry) bool {
	if !m.errorsOnly {
		return true
	}
	return e.req == nil || e.req.Status >= 400
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
		m.clampScroll()
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case flashMsg:
		if int(msg) == m.flashID {
			m.flash = ""
		}
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
	case stopMsg:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := max(1, m.logHeight()-1)
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	case "o":
		url := m.srv.URL()
		if err := openBrowser(url); err != nil {
			return m, m.setFlash("could not open browser: " + err.Error())
		}
		return m, m.setFlash("opened " + url)
	case "r":
		m.srv.SetReadOnly(!m.srv.ReadOnly())
		if m.srv.ReadOnly() {
			return m, m.setFlash("now read-only: write, remove and upload are disabled")
		}
		return m, m.setFlash("now read-write")
	case "e":
		m.errorsOnly = !m.errorsOnly
		m.scroll = 0
	case "c":
		m.entries = nil
		m.scroll = 0
		m.requests, m.errors, m.bytesOut = 0, 0, 0
		return m, m.setFlash("log cleared")
	case "up", "k":
		m.scroll++
	case "down", "j":
		m.scroll--
	case "pgup", "ctrl+u":
		m.scroll += page
	case "pgdown", "ctrl+d", " ":
		m.scroll -= page
	case "home", "g":
		m.scroll = len(m.entries)
	case "end", "G", "f":
		m.scroll = 0
	}
	m.clampScroll()
	return m, nil
}

func (m *model) clampScroll() {
	maxScroll := max(0, m.countVisible()-m.logHeight())
	m.scroll = min(max(m.scroll, 0), maxScroll)
}

func (m model) countVisible() int {
	if !m.errorsOnly {
		return len(m.entries)
	}
	n := 0
	for _, e := range m.entries {
		if m.visible(e) {
			n++
		}
	}
	return n
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
	labelStyle  = lipgloss.NewStyle().Foreground(subtle).Width(7)
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
		row("URL", urlStyle.Render(m.srv.URL())),
	}
	for _, u := range m.srv.LANURLs() {
		lines = append(lines, row("LAN", urlStyle.Render(u)))
	}
	lines = append(lines, row("Root", valueStyle.Render(m.srv.Root())))

	mode := badge("read-write", green)
	if m.srv.ReadOnly() {
		mode = badge("read-only", yellow)
	}
	access := dimStyle.Render("local only")
	if m.srv.Global() {
		access = badge("network-visible", yellow)
	}
	sep := dimStyle.Render("  │  ")
	lines = append(lines, row("Mode", mode+sep+access))

	errs := dimStyle.Render("0 errors")
	if m.errors > 0 {
		errs = badge(fmt.Sprintf("%d errors", m.errors), red)
	}
	lines = append(lines, row("Stats",
		valueStyle.Render(fmt.Sprintf("%d requests", m.requests))+sep+errs+sep+
			valueStyle.Render(humanBytes(m.bytesOut)+" sent")))

	if m.srv.Global() && !m.srv.ReadOnly() {
		lines = append(lines, " "+badge("⚠ anyone on your network can modify files in Root (press r for read-only)", yellow))
	}
	return lines
}

func (m model) footerLines() []string {
	keys := []struct{ k, d string }{
		{"o", "open"}, {"r", "read-only"}, {"e", "errors"}, {"c", "clear"},
		{"↑↓", "scroll"}, {"f", "follow"}, {"q", "quit"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, keyStyle.Render(k.k)+" "+dimStyle.Render(k.d))
	}
	help := " " + strings.Join(parts, dimStyle.Render(" · "))
	status := " "
	if m.flash != "" {
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
		return " " + ts + "  " + logStyle.Render(strings.TrimRight(e.log, "\n"))
	}
	r := e.req
	return fmt.Sprintf(" %s  %s %s %s %s  %s",
		ts,
		methodStyle.Render(r.Method),
		statusStyle(r.Status).Render(fmt.Sprintf("%3d", r.Status)),
		dimStyle.Render(fmt.Sprintf("%8s", humanDuration(r.Duration))),
		dimStyle.Render(fmt.Sprintf("%8s", humanBytes(r.Bytes))),
		valueStyle.Render(r.Path),
	)
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
	left := titleStyle.Render("myweb") + " " + dimStyle.Render("v"+myserver.VERSION)
	right := badge("● running", green) + "  " + dimStyle.Render("up "+uptime.String()) + " "
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	line(left + strings.Repeat(" ", gap) + right)
	line(m.rule(""))

	for _, l := range m.infoLines() {
		line(l)
	}

	// request log
	title := "Requests"
	if m.errorsOnly {
		title += " (errors only)"
	}
	if m.scroll > 0 {
		title += fmt.Sprintf(" · paused, %d newer below", m.scroll)
	}
	line(m.rule(title))

	h := m.logHeight()
	var visible []entry
	for _, e := range m.entries {
		if m.visible(e) {
			visible = append(visible, e)
		}
	}
	end := len(visible) - m.scroll
	start := max(0, end-h)
	shown := 0
	if len(visible) == 0 {
		line(dimStyle.Render("  waiting for requests…"))
		shown = 1
	} else {
		for _, e := range visible[start:end] {
			line(m.renderEntry(e))
			shown++
		}
	}
	for ; shown < h; shown++ {
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
