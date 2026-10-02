package console

import (
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nimezhu/myweb/myserver"
)

func testModel(t *testing.T, height int) model {
	srv := myserver.New(myserver.Config{Root: t.TempDir()})
	m := newModel(srv, Options{Name: "myweb", Version: "test"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: height})
	return next.(model)
}

func send(m model, msgs ...tea.Msg) model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func key(s string) tea.Msg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func req(method, path string, status int, errBody string) tea.Msg {
	return reqMsg(myserver.RequestEvent{Time: time.Now(), Method: method, Path: path, Status: status, Error: errBody, Remote: "127.0.0.1:1"})
}

func TestFilter(t *testing.T) {
	m := testModel(t, 30)
	m = send(m, req("GET", "/f/a.txt", 200, ""), req("POST", "/w/b.txt", 200, ""), req("GET", "/f/b.png", 404, "not found"))
	m = send(m, key("/"), key("b"), key("."), key("p"), key("backspace"))
	if !m.typing || m.filter != "b." {
		t.Fatalf("typing filter: %v %q", m.typing, m.filter)
	}
	if !strings.Contains(m.View(), "/ b.") {
		t.Fatal("filter prompt not shown")
	}
	m = send(m, key("enter"))
	if v := m.visibleEntries(); len(v) != 2 {
		t.Fatalf("filter b.: %d entries", len(v))
	}
	m = send(m, key("e")) // combined with errors-only
	if v := m.visibleEntries(); len(v) != 1 || v[0].req.Status != 404 {
		t.Fatalf("filter + errors: %v", v)
	}
	if !strings.Contains(m.View(), `filter "b."`) {
		t.Fatal("filter not in title")
	}
	m = send(m, key("esc"))
	if m.filter != "" || m.errorsOnly || len(m.visibleEntries()) != 3 {
		t.Fatal("esc should clear filter and errors-only")
	}
	m = send(m, key("/"), key("zzz"), key("enter"))
	if !strings.Contains(m.View(), "nothing matches") {
		t.Fatal("empty filter result message")
	}
}

func TestSelectionAndDetail(t *testing.T) {
	m := testModel(t, 30)
	m = send(m, req("GET", "/a", 200, ""), req("POST", "/w/x", 401, `{"success":0,"error":"missing or invalid token"}`), req("GET", "/c", 200, ""))
	m = send(m, key("up"))
	if v := m.visibleEntries(); m.sel != v[2].id {
		t.Fatal("first up should select the newest")
	}
	m = send(m, key("up"), key("enter"))
	view := m.View()
	if !m.detail || !strings.Contains(view, "401 Unauthorized") || !strings.Contains(view, "missing or invalid token") {
		t.Fatalf("detail view:\n%s", view)
	}
	m = send(m, key("down")) // next entry, still in detail
	if !m.detail || !strings.Contains(m.View(), "GET /c") {
		t.Fatal("down in detail should show the next entry")
	}
	m = send(m, key("esc"))
	if m.detail || m.sel == 0 {
		t.Fatal("esc: detail closes, selection stays")
	}
	// new requests while selected: view stays, title reports them
	for i := 0; i < 40; i++ {
		m = send(m, req("GET", "/more", 200, ""))
	}
	if !strings.Contains(m.View(), "newer below") {
		t.Fatal("paused indicator missing")
	}
	m = send(m, key("esc"))
	if m.sel != 0 || strings.Contains(m.View(), "newer below") {
		t.Fatal("esc: back to following")
	}
	// moving past the end returns to following
	m = send(m, key("up"), key("down"))
	if m.sel != 0 {
		t.Fatal("down past the end should follow")
	}
	// esc on a plain view doesn't quit (q does)
	if _, cmd := m.Update(key("esc")); cmd != nil {
		t.Fatal("esc should not quit")
	}
	if next, _ := m.Update(key("q")); !next.(model).quitting {
		t.Fatal("q should quit")
	}
}

func TestSelectionSurvivesTrimming(t *testing.T) {
	m := testModel(t, 30)
	m = send(m, req("GET", "/first", 200, ""), key("up"))
	for i := 0; i < maxEntries; i++ {
		m = send(m, req("GET", "/x", 200, ""))
	}
	if m.sel != 0 || m.detail {
		t.Fatal("selection of a dropped entry should fall back to following")
	}
}

func TestQR(t *testing.T) {
	m := testModel(t, 40)
	m = send(m, key("u"))
	if v := m.View(); !strings.Contains(v, "▀") || !strings.Contains(v, "start with -global") {
		t.Fatalf("qr view:\n%s", v)
	}
	small := send(testModel(t, 14), key("u"))
	if !strings.Contains(small.View(), "make the terminal larger") {
		t.Fatal("small terminal message")
	}
	m = send(m, key("u"))
	if m.qr {
		t.Fatal("u toggles qr off")
	}
}

func TestManagerKeyAndRow(t *testing.T) {
	d := t.TempDir()
	app := myserver.New(myserver.Config{Root: d, WebFS: http.Dir(d), Manager: http.Dir(d)})
	m := newModel(app, Options{Name: "myweb", Version: "test"})
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if v := m.View(); !strings.Contains(v, "/_myweb/") || !strings.Contains(v, "m files") {
		t.Fatalf("app + manager: Files row and m key expected:\n%s", v)
	}
	plain := testModel(t, 30) // no web app, no manager configured
	if v := plain.View(); strings.Contains(v, "m files") {
		t.Fatal("no m key without a separate manager")
	}
}
