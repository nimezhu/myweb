package console

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nimezhu/myweb/webapp"
)

type fakeVersions struct {
	fakeApp
	st   webapp.Status
	used []string
}

func (f *fakeVersions) Status() webapp.Status { return f.st }
func (f *fakeVersions) Available(context.Context) (*webapp.Versions, error) {
	return &webapp.Versions{Name: "demo", Versions: []webapp.Manifest{
		{Version: "1.2.0", Notes: "newest", Published: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{Version: "1.1.0", Notes: "faster"}, {Version: "1.0.0", Notes: "first"},
	}}, nil
}
func (f *fakeVersions) Use(_ context.Context, v string) error {
	f.used = append(f.used, v)
	if v == "latest" {
		f.st.Pinned, f.st.Current = "", "1.2.0"
	} else {
		f.st.Pinned, f.st.Current = v, v
	}
	return nil
}

// run applies a command's message, as the TUI loop would.
func run(m model, cmd tea.Cmd) model {
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m = send(m, msg)
		}
	}
	return m
}

func TestAppView(t *testing.T) {
	m := testModel(t, 30)
	app := &fakeVersions{st: webapp.Status{Name: "demo", Current: "1.1.0", Status: "up to date", Updates: true,
		Manifest: "https://dist.example.com/demo/stable.json", Installed: []string{"1.1.0", "1.0.0"},
		Notes: map[string]string{"1.1.0": "faster", "1.0.0": "first"}}}
	m.opts.App = app
	if !strings.Contains(m.View(), "a: versions") {
		t.Fatal("hint")
	}
	m = send(m, key("a"))
	v := m.View()
	for _, want := range []string{"Installed versions", "1.1.0", "serving", "1.0.0", "first", "⏎ use version"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q in\n%s", want, v)
		}
	}
	if os.Getenv("SHOW_TUI") != "" {
		t.Log("\n" + v)
	}
	// select 1.0.0 and use it
	m = send(m, key("j"))
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = run(mm.(model), cmd)
	if strings.Join(app.used, ",") != "1.0.0" || !strings.Contains(m.View(), "now serving 1.0.0") {
		t.Fatalf("use: %v\n%s", app.used, m.View())
	}
	// all published, then follow updates again
	mm, cmd = m.Update(key("p"))
	m = run(mm.(model), cmd)
	if v := m.View(); !strings.Contains(v, "Published versions") || !strings.Contains(v, "published 2026-10-01") {
		t.Fatalf("published:\n%s", v)
	}
	if os.Getenv("SHOW_TUI") != "" {
		t.Log("\n" + m.View())
	}
	mm, cmd = m.Update(key("l"))
	m = run(mm.(model), cmd)
	if app.used[len(app.used)-1] != "latest" || !strings.Contains(m.View(), "following updates") {
		t.Fatalf("latest: %v", app.used)
	}
	m = send(m, key("esc"))
	if strings.Contains(m.View(), "Installed versions") || strings.Contains(m.View(), "Published versions") {
		t.Fatal("esc should close the app view")
	}
}
