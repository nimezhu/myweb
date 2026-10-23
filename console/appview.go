package console

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nimezhu/myweb/webapp"
)

// The app view ('a'): the installed web app's status, release notes and
// versions, with keys to check, switch versions and follow updates again.

type appDoneMsg struct{ flash string }
type appAvailMsg struct {
	list []webapp.Manifest
	err  error
}

type appRow struct {
	version, notes, where string
}

func (m model) appVersions() (AppVersions, bool) {
	av, ok := m.opts.App.(AppVersions)
	return av, ok
}

// appRows are the installed versions, or every published one after 'p'.
func (m model) appRows(st webapp.Status) []appRow {
	var rows []appRow
	if m.appAvail != nil {
		for _, v := range m.appAvail {
			where := "published " + v.Published.Format("2006-01-02")
			if containsStr(st.Installed, v.Version) {
				where = "installed"
			}
			rows = append(rows, appRow{v.Version, v.Notes, where})
		}
		return rows
	}
	for _, v := range st.Installed {
		rows = append(rows, appRow{v, st.Notes[v], "installed"})
	}
	return rows
}

func (m model) appKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	av, _ := m.appVersions()
	st := av.Status()
	rows := m.appRows(st)
	switch msg.String() {
	case "esc", "a":
		m.appView = false
	case "up", "k":
		m.appSel = max(0, m.appSel-1)
	case "down", "j":
		m.appSel = min(max(0, len(rows)-1), m.appSel+1)
	case "c":
		m.opts.App.CheckNow()
		return m, m.setFlash("checking for an app update…"), true
	case "p":
		m.appBusy = "listing published versions…"
		return m, func() tea.Msg {
			vs, err := av.Available(context.Background())
			if err != nil {
				return appAvailMsg{err: err}
			}
			return appAvailMsg{list: vs.Versions}
		}, true
	case "l":
		if st.Pinned == "" {
			return m, m.setFlash("already following updates"), true
		}
		return m.appUse(av, "latest")
	case "enter":
		if m.appSel >= len(rows) {
			return m, nil, true
		}
		v := rows[m.appSel].version
		if v == st.Current {
			return m, m.setFlash(v + " is already served"), true
		}
		return m.appUse(av, v)
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m model) appUse(av AppVersions, v string) (tea.Model, tea.Cmd, bool) {
	m.appBusy = "switching to " + v + "…"
	return m, func() tea.Msg {
		if err := av.Use(context.Background(), v); err != nil {
			return appDoneMsg{"could not switch: " + err.Error()}
		}
		st := av.Status()
		if st.Pinned != "" {
			return appDoneMsg{fmt.Sprintf("now serving %s; updates are off (l follows them again)", st.Current)}
		}
		return appDoneMsg{"following updates; serving " + st.Current}
	}, true
}

func (m model) appLines(h int) []string {
	av, _ := m.appVersions()
	st := av.Status()
	w := max(20, m.width)
	row := func(label, value string) string {
		return " " + labelStyle.Render(label) + value
	}
	state := valueStyle.Render(st.Status)
	switch {
	case st.Pinned != "":
		state = badge("pinned to "+st.Pinned, yellow) + dimStyle.Render("  (l follows updates)")
	case st.Status == "up to date":
		state = badge("up to date", green)
	case st.Checking || strings.HasSuffix(st.Status, "…"):
		state = badge(st.Status, yellow)
	case st.Status != "" && st.Status != "installed":
		state = badge(st.Status, red)
	}
	if st.Checked != nil {
		state += dimStyle.Render("  checked " + st.Checked.Format("15:04"))
	}
	name := st.Name
	if name == "" {
		name = "app"
	}
	cur := st.Current
	if cur == "" {
		cur = "not installed"
	}
	out := []string{
		row("App", valueStyle.Bold(true).Render(name)+" "+valueStyle.Render(cur)),
		row("Status", state),
		row("From", dimStyle.Render(st.Manifest)),
	}
	if st.Latest != "" && st.Latest != st.Current && !containsStr(st.Installed, st.Latest) {
		out = append(out, row("Latest", badge(st.Latest, cyan)+dimStyle.Render(" published, not installed")))
	}
	if n := strings.TrimSpace(st.Notes[st.Current]); n != "" {
		for i, l := range strings.Split(n, "\n") {
			label := ""
			if i == 0 {
				label = "Notes"
			}
			out = append(out, row(label, valueStyle.Render(l)))
		}
	}
	if m.appBusy != "" {
		out = append(out, "", " "+flashStyle.Render(m.appBusy))
	}

	rows := m.appRows(st)
	title := "Installed versions"
	if m.appAvail != nil {
		title = "Published versions"
	} else {
		title += dimStyle.Render("  (p: all published)")
	}
	out = append(out, "", " "+valueStyle.Bold(true).Render(title))
	room := max(1, h-len(out))
	start := 0
	if m.appSel >= room {
		start = m.appSel - room + 1
	}
	notesW := max(10, w-36)
	for i := start; i < len(rows) && i < start+room; i++ {
		r := rows[i]
		marker := "  "
		if i == m.appSel {
			marker = " " + keyStyle.Render("›")
		}
		tag := dimStyle.Render(fmt.Sprintf("%-22s", r.where))
		if r.version == st.Current {
			tag = badge(fmt.Sprintf("%-22s", "serving"), green)
		}
		notes := strings.SplitN(strings.TrimSpace(r.notes), "\n", 2)[0]
		out = append(out, marker+" "+valueStyle.Render(fmt.Sprintf("%-10s", r.version))+" "+tag+" "+
			lipgloss.NewStyle().MaxWidth(notesW).Foreground(subtle).Render(notes))
	}
	if len(rows) == 0 {
		out = append(out, dimStyle.Render("   nothing installed yet"))
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
