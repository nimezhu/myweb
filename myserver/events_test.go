package myserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wsT struct {
	t *testing.T
	c *websocket.Conn
}

func dialEvents(t *testing.T, ts *httptest.Server, hdr http.Header) *wsT {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/events", hdr)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial: %v (%d)", err, code)
	}
	t.Cleanup(func() { c.Close() })
	w := &wsT{t, c}
	if m := w.next(); m["type"] != "hello" {
		t.Fatalf("want hello, got %v", m)
	}
	return w
}

func (w *wsT) send(v interface{}) { w.c.WriteJSON(v) }

func (w *wsT) next() map[string]interface{} {
	w.t.Helper()
	w.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m map[string]interface{}
	if err := w.c.ReadJSON(&m); err != nil {
		w.t.Fatalf("read: %v", err)
	}
	return m
}

// nextChange skips messages until a change for p arrives.
func (w *wsT) nextChange(p string) map[string]interface{} {
	w.t.Helper()
	for {
		m := w.next()
		if m["type"] == "change" && m["path"] == p {
			return m
		}
	}
}

// expectNone asserts nothing arrives for a while.
func (w *wsT) expectNone(d time.Duration) {
	w.t.Helper()
	w.c.SetReadDeadline(time.Now().Add(d))
	var m map[string]interface{}
	if err := w.c.ReadJSON(&m); err == nil {
		w.t.Fatalf("unexpected message %v", m)
	}
}

func TestEventsWatchDir(t *testing.T) {
	s, ts, root := newTestServer(t)
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	w := dialEvents(t, ts, nil)
	w.send(map[string]interface{}{"op": "watch", "path": "/notes"})
	if m := w.next(); m["type"] != "watching" || m["path"] != "/notes" {
		t.Fatalf("watch ack: %v", m)
	}
	// a write through the API: one change, temp file hidden
	post(t, ts.URL+"/w/notes/a.md", "", strings.NewReader("hi"), nil)
	if m := w.nextChange("/notes/a.md"); m["exists"] != true || m["isdir"] != false {
		t.Fatalf("change: %v", m)
	}
	// an outside edit
	os.WriteFile(filepath.Join(root, "notes", "b.txt"), []byte("x"), 0o644)
	w.nextChange("/notes/b.txt")
	os.Remove(filepath.Join(root, "notes", "b.txt"))
	if m := w.nextChange("/notes/b.txt"); m["exists"] != false {
		t.Fatalf("remove: %v", m)
	}
	// non-recursive: nothing from a subdirectory's contents, nor from elsewhere
	os.MkdirAll(filepath.Join(root, "notes", "sub"), 0o755)
	w.nextChange("/notes/sub")
	os.WriteFile(filepath.Join(root, "notes", "sub", "c.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "other.txt"), []byte("x"), 0o644)
	w.expectNone(400 * time.Millisecond)
	// unwatch stops events
	w.send(map[string]interface{}{"op": "unwatch", "path": "/notes"})
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "notes", "d.txt"), []byte("x"), 0o644)
	w.expectNone(400 * time.Millisecond)
	s.events.mu.Lock()
	n := len(s.events.dirs)
	s.events.mu.Unlock()
	if n != 0 {
		t.Fatalf("watches leaked after unwatch: %d", n)
	}
}

func TestEventsRecursiveAndFile(t *testing.T) {
	_, ts, root := newTestServer(t)
	os.MkdirAll(filepath.Join(root, "p", "q"), 0o755)
	os.WriteFile(filepath.Join(root, "k.json"), []byte("{}"), 0o644)
	w := dialEvents(t, ts, nil)
	w.send(map[string]interface{}{"op": "watch", "path": "/p", "recursive": true})
	w.next()
	w.send(map[string]interface{}{"op": "watch", "path": "/k.json"})
	w.next()
	os.WriteFile(filepath.Join(root, "p", "q", "deep.txt"), []byte("x"), 0o644)
	w.nextChange("/p/q/deep.txt")
	// a directory created after subscribing is watched too
	os.MkdirAll(filepath.Join(root, "p", "new"), 0o755)
	w.nextChange("/p/new")
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "p", "new", "n.txt"), []byte("x"), 0o644)
	w.nextChange("/p/new/n.txt")
	// a file watch: only that file
	os.WriteFile(filepath.Join(root, "k.json"), []byte(`{"a":1}`), 0o644)
	w.nextChange("/k.json")
	os.WriteFile(filepath.Join(root, "sibling.txt"), []byte("x"), 0o644)
	w.expectNone(400 * time.Millisecond)
}

func TestEventsErrorsAndStatus(t *testing.T) {
	s, ts, _ := newTestServer(t)
	w := dialEvents(t, ts, nil)
	w.send(map[string]interface{}{"op": "watch", "path": "/missing"})
	if m := w.next(); m["type"] != "error" || m["error"] != "not found" {
		t.Fatalf("missing: %v", m)
	}
	w.send(map[string]interface{}{"op": "watch", "path": "/../../etc"})
	if m := w.next(); m["type"] == "watching" && m["path"] != "/etc" {
		t.Fatalf("escape: %v", m)
	}
	w.c.WriteMessage(websocket.TextMessage, []byte("not json"))
	if m := w.next(); m["type"] != "error" {
		t.Fatalf("bad json: %v", m)
	}
	s.SetReadOnly(true)
	for {
		if m := w.next(); m["type"] == "status" {
			if m["readOnly"] != true {
				t.Fatalf("status: %v", m)
			}
			break
		}
	}
	if s.EventClients() != 1 {
		t.Fatalf("clients: %d", s.EventClients())
	}
	w.c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for s.EventClients() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.EventClients() != 0 {
		t.Fatal("client not removed after disconnect")
	}
}

func TestEventsAuthAndOrigin(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, Token: tok, Global: true, CORS: []string{"https://ok.example"}})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)

	w := dialEvents(t, ts, nil)
	w.send(map[string]interface{}{"op": "watch", "path": "/"})
	if m := w.next(); m["type"] != "error" {
		t.Fatalf("global watch without token: %v", m)
	}
	w.send(map[string]interface{}{"op": "auth", "token": "wrong"})
	if m := w.next(); m["type"] != "error" {
		t.Fatalf("bad token: %v", m)
	}
	w.send(map[string]interface{}{"op": "auth", "token": tok})
	if m := w.next(); m["type"] != "authed" {
		t.Fatalf("auth: %v", m)
	}
	w.send(map[string]interface{}{"op": "watch", "path": "/"})
	if m := w.next(); m["type"] != "watching" {
		t.Fatalf("watch after auth: %v", m)
	}
	// header auth at connect time
	w2 := dialEvents(t, ts, http.Header{"Authorization": {"Bearer " + tok}})
	w2.send(map[string]interface{}{"op": "watch", "path": "/"})
	if m := w2.next(); m["type"] != "watching" {
		t.Fatalf("bearer watch: %v", m)
	}

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/events"
	if _, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://ok.example"}}); err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	if _, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://evil.example"}}); err == nil || resp.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
}

func TestEventsShutdownClosesClients(t *testing.T) {
	s, ts, _ := newTestServer(t)
	w := dialEvents(t, ts, nil)
	s.events.close()
	w.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := w.c.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway) && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "EOF") {
				t.Fatalf("unexpected close: %v", err)
			}
			break
		}
	}
	_ = json.Marshal
}
