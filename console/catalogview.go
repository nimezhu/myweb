package console

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The apps view ('i'): the catalogs' apps, to install and open.

// Catalog is what the apps view lists and does.
type Catalog interface {
	List(ctx context.Context, refresh bool) ([]CatalogItem, error)
	Install(ctx context.Context, name string) error
	// Open returns the app's URL, starting it if needed.
	Open(name string) (string, error)
}

// CatalogItem is one app in the apps view.
type CatalogItem struct {
	Name, Title, Description string
	Tags                     []string
	Installed                string // its version, "" when not installed
	URL                      string // where it is served, if it is
}

// openURL opens an app the apps view opened; replaced in tests.
var openURL = OpenBrowser

// SetCatalog attaches the catalogs (call before Run).
func (c *Console) SetCatalog(cat Catalog) { c.opts.Catalog = cat }

type catListMsg struct {
	items []CatalogItem
	err   error
}

type catDoneMsg struct {
	flash string
	open  string // a URL to open
}

func (m model) catLoad(refresh bool) tea.Cmd {
	cat := m.opts.Catalog
	return func() tea.Msg {
		items, err := cat.List(context.Background(), refresh)
		return catListMsg{items, err}
	}
}

func (m model) catKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "i":
		m.catView = false
	case "up", "k":
		m.catSel = max(0, m.catSel-1)
	case "down", "j":
		m.catSel = min(max(0, len(m.catItems)-1), m.catSel+1)
	case "r":
		m.catBusy = "refreshing the catalogs…"
		return m, m.catLoad(true), true
	case "enter":
		if m.catSel >= len(m.catItems) || m.catBusy != "" {
			return m, nil, true
		}
		it := m.catItems[m.catSel]
		cat := m.opts.Catalog
		if it.Installed == "" {
			m.catBusy = "installing " + it.Name + "…"
			return m, func() tea.Msg {
				if err := cat.Install(context.Background(), it.Name); err != nil {
					return catDoneMsg{flash: "could not install " + it.Name + ": " + err.Error()}
				}
				return catDoneMsg{flash: it.Name + " installed; ⏎ opens it"}
			}, true
		}
		m.catBusy = "opening " + it.Name + "…"
		return m, func() tea.Msg {
			u, err := cat.Open(it.Name)
			if err != nil {
				return catDoneMsg{flash: "could not open " + it.Name + ": " + err.Error()}
			}
			return catDoneMsg{flash: "opened " + it.Name, open: u}
		}, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m model) catLines(h int) []string {
	w := max(20, m.width)
	var out []string
	if m.catBusy != "" {
		out = append(out, " "+flashStyle.Render(m.catBusy), "")
	}
	if m.catErr != "" {
		out = append(out, " "+lipgloss.NewStyle().Foreground(red).Render(m.catErr), "")
	}
	if m.catItems == nil {
		return append(out, dimStyle.Render("   loading the catalogs…"))
	}
	if len(m.catItems) == 0 {
		return append(out, dimStyle.Render("   the catalogs list no apps (myweb app catalog add URL -key KEY adds one)"))
	}
	// the selected app's description and tags at the bottom
	var about []string
	if m.catSel < len(m.catItems) {
		it := m.catItems[m.catSel]
		if it.Description != "" {
			about = append(about, " "+lipgloss.NewStyle().MaxWidth(w-2).Foreground(subtle).Render(it.Description))
		}
		if len(it.Tags) > 0 {
			about = append(about, " "+dimStyle.Render(strings.Join(it.Tags, " · ")))
		}
		if it.URL != "" {
			about = append(about, " "+urlStyle.Render(it.URL))
		}
	}
	room := max(1, h-len(out)-len(about)-1)
	start := 0
	if m.catSel >= room {
		start = m.catSel - room + 1
	}
	for i := start; i < len(m.catItems) && i < start+room; i++ {
		it := m.catItems[i]
		marker := "  "
		if i == m.catSel {
			marker = " " + keyStyle.Render("›")
		}
		state := dimStyle.Render(fmt.Sprintf("%-16s", "not installed"))
		switch {
		case it.URL != "":
			state = badge(fmt.Sprintf("%-16s", it.Installed+" · served"), green)
		case it.Installed != "":
			state = badge(fmt.Sprintf("%-16s", it.Installed), cyan)
		}
		title := it.Title
		if title == "" {
			title = it.Name
		}
		out = append(out, marker+" "+valueStyle.Render(fmt.Sprintf("%-18s", it.Name))+" "+state+" "+
			lipgloss.NewStyle().MaxWidth(max(10, w-42)).Foreground(subtle).Render(title))
	}
	if len(about) > 0 {
		out = append(out, "")
		out = append(out, about...)
	}
	return out
}
