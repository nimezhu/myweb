package myserver

// File-change events over WebSocket at /events.
//
// Clients subscribe to the paths they show; only those directories are
// watched (inotify has a per-user limit, and data roots can be huge).
//
//	client → server  {"op":"watch","path":"/notes","recursive":false}
//	                 {"op":"unwatch","path":"/notes"}
//	                 {"op":"auth","token":"…"}   (browsers can't set headers on a WebSocket)
//	server → client  {"type":"hello","version":"…","readOnly":false,"auth":false,"authed":true}
//	                 {"type":"watching","path":"/notes"}
//	                 {"type":"change","path":"/notes/a.md","exists":true,"isdir":false}
//	                 {"type":"status","readOnly":true}
//	                 {"type":"error","error":"…","path":"/notes"}
//
// Changes are coalesced for a short window, so an editor's save (or our own
// temp-file + rename) arrives as one "change" with the final state.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

var (
	eventCoalesce    = 150 * time.Millisecond
	maxWatchedDirs   = 4096 // across all clients
	maxSubsPerClient = 64
	wsPingInterval   = 30 * time.Second
	wsReadTimeout    = 70 * time.Second
	wsWriteTimeout   = 10 * time.Second
)

// temp files from writeFileAtomic: ".name.tmp123"
var tmpFileRe = regexp.MustCompile(`^\..+\.tmp\d+$`)

type subscription struct {
	urlPath   string
	target    string // fs path (dir, or file when file != "")
	file      bool
	recursive bool
	dirs      []string // watches this subscription holds
}

// covers reports whether a change at fsPath concerns this subscription.
func (sub *subscription) covers(fsPath string) bool {
	if sub.file {
		return fsPath == sub.target
	}
	if fsPath == sub.target || filepath.Dir(fsPath) == sub.target {
		return true
	}
	return sub.recursive && strings.HasPrefix(fsPath, sub.target+string(filepath.Separator))
}

type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	subs   map[string]*subscription // by url path
	authed bool
	user   *User         // multi-user: who is watching; events are filtered for them
	req    *http.Request // to identify a token sent later with {"op":"auth"}
	once   sync.Once
}

func (c *wsClient) close() { c.once.Do(func() { c.conn.Close() }) }

type eventHub struct {
	s       *Server
	mu      sync.Mutex
	w       *fsnotify.Watcher // created on first watch
	dirs    map[string]int    // watched fs dir → refcount
	clients map[*wsClient]struct{}
	pending map[string]struct{}
	timer   *time.Timer
	closed  bool
}

func newEventHub(s *Server) *eventHub {
	return &eventHub{
		s:       s,
		dirs:    map[string]int{},
		clients: map[*wsClient]struct{}{},
		pending: map[string]struct{}{},
	}
}

// EventClients is the number of connected /events clients.
func (s *Server) EventClients() int {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	return len(s.events.clients)
}

func (s *Server) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		// Browsers don't apply CORS to WebSockets, so check the origin here:
		// same origin, non-browser clients, or an allowed CORS origin.
		CheckOrigin: func(r *http.Request) bool {
			if r.Header.Get("Origin") == "" || sameOrigin(r) {
				return true
			}
			return s.cors != nil && s.cors.OriginAllowed(r)
		},
	}
}

// watchAllowed: reads of file names need the token only when reads do.
func (s *Server) watchAllowed(c *wsClient) bool {
	if s.cfg.Access != nil {
		return c.user != nil
	}
	return c.authed || !(s.AuthEnabled() && s.cfg.Global)
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader().Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already replied
	}
	c := &wsClient{
		conn:   conn,
		send:   make(chan []byte, 256),
		subs:   map[string]*subscription{},
		authed: s.Authorized(r),
		user:   UserFrom(r.Context()),
		req:    r,
	}
	h := s.events
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		conn.Close()
		return
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	go h.writePump(c)
	h.sendTo(c, map[string]interface{}{
		"type": "hello", "version": VERSION, "readOnly": s.ReadOnly(),
		"auth": s.AuthEnabled(), "authed": c.authed,
	})
	h.readLoop(c) // returns when the client goes away
	h.drop(c)
}

func (h *eventHub) readLoop(c *wsClient) {
	c.conn.SetReadLimit(64 << 10)
	c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		var msg struct {
			Op        string `json:"op"`
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
			Token     string `json:"token"`
		}
		if json.Unmarshal(data, &msg) != nil {
			h.sendError(c, "", "messages must be JSON")
			continue
		}
		switch msg.Op {
		case "auth":
			if a := h.s.cfg.Access; a != nil { // a personal token
				if u := a.Identify(c.req, msg.Token); u != nil {
					h.mu.Lock()
					c.user, c.authed = u, true
					h.mu.Unlock()
					h.sendTo(c, map[string]interface{}{"type": "authed", "user": u.Name})
				} else {
					h.sendError(c, "", "invalid token")
				}
			} else if !h.s.AuthEnabled() || h.s.tokenOK(msg.Token) {
				c.authed = true
				h.sendTo(c, map[string]interface{}{"type": "authed"})
			} else {
				h.sendError(c, "", "invalid token")
			}
		case "watch":
			if !h.s.watchAllowed(c) {
				h.sendError(c, msg.Path, "missing or invalid token")
				continue
			}
			if err := h.watch(c, msg.Path, msg.Recursive); err != nil {
				h.sendError(c, msg.Path, err.Error())
				continue
			}
			h.sendTo(c, map[string]interface{}{"type": "watching", "path": cleanURLPath(msg.Path)})
		case "unwatch":
			h.unwatch(c, cleanURLPath(msg.Path))
		case "ping":
			h.sendTo(c, map[string]interface{}{"type": "pong"})
		default:
			h.sendError(c, "", "unknown op: "+msg.Op)
		}
	}
}

func (h *eventHub) writePump(c *wsClient) {
	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()
	defer c.close()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.SetWriteDeadline(time.Now().Add(time.Second))
				c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ping.C:
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendTo queues a message; a client that can't keep up is disconnected
// rather than allowed to stall everyone else.
func (h *eventHub) sendTo(c *wsClient, v interface{}) {
	data, _ := json.Marshal(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queueLocked(c, data)
}

func (h *eventHub) queueLocked(c *wsClient, data []byte) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	select {
	case c.send <- data:
	default:
		h.removeClientLocked(c)
	}
}

func (h *eventHub) sendError(c *wsClient, p, msg string) {
	m := map[string]interface{}{"type": "error", "error": msg}
	if p != "" {
		m["path"] = cleanURLPath(p)
	}
	h.sendTo(c, m)
}

func (h *eventHub) broadcast(v interface{}) {
	data, _ := json.Marshal(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		h.queueLocked(c, data)
	}
}

func cleanURLPath(p string) string { return path.Clean("/" + p) }

func (h *eventHub) watch(c *wsClient, urlPath string, recursive bool) error {
	urlPath = cleanURLPath(urlPath)
	target := h.s.ResolvePath(urlPath)
	fi, err := os.Stat(target)
	if err == nil && !h.s.visible(c.user, urlPath, fi.IsDir()) {
		err = fs.ErrNotExist // multi-user: don't reveal what you may not see
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("not found")
		}
		return errors.New("cannot access path")
	}
	sub := &subscription{urlPath: urlPath, target: target, file: !fi.IsDir(), recursive: recursive && fi.IsDir()}

	// collect directories outside the lock (walking can be slow)
	var dirs []string
	switch {
	case sub.file:
		dirs = []string{filepath.Dir(target)}
	case sub.recursive:
		err = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if p != target && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if len(dirs) >= maxWatchedDirs {
				return errors.New("too many directories to watch recursively")
			}
			dirs = append(dirs, p)
			return nil
		})
		if err != nil {
			return err
		}
	default:
		dirs = []string{target}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("server is shutting down")
	}
	if old, ok := c.subs[urlPath]; ok {
		h.releaseLocked(old)
		delete(c.subs, urlPath)
	}
	if len(c.subs) >= maxSubsPerClient {
		return errors.New("too many watches on this connection")
	}
	if h.w == nil {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return errors.New("file watching unavailable: " + err.Error())
		}
		h.w = w
		go h.run(w)
	}
	for _, d := range dirs {
		if err := h.addDirLocked(d); err != nil {
			h.releaseLocked(sub)
			return err
		}
		sub.dirs = append(sub.dirs, d)
	}
	c.subs[urlPath] = sub
	return nil
}

func (h *eventHub) addDirLocked(d string) error {
	if h.dirs[d] == 0 {
		if len(h.dirs) >= maxWatchedDirs {
			return errors.New("watch limit reached")
		}
		if err := h.w.Add(d); err != nil {
			return errors.New("cannot watch: " + err.Error())
		}
	}
	h.dirs[d]++
	return nil
}

func (h *eventHub) releaseLocked(sub *subscription) {
	for _, d := range sub.dirs {
		h.dirs[d]--
		if h.dirs[d] <= 0 {
			delete(h.dirs, d)
			if h.w != nil {
				h.w.Remove(d) // may already be gone with the directory
			}
		}
	}
	sub.dirs = nil
}

func (h *eventHub) unwatch(c *wsClient, urlPath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub, ok := c.subs[urlPath]; ok {
		h.releaseLocked(sub)
		delete(c.subs, urlPath)
	}
}

func (h *eventHub) removeClientLocked(c *wsClient) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	for _, sub := range c.subs {
		h.releaseLocked(sub)
	}
	delete(h.clients, c)
	close(c.send) // writePump sends a close frame and exits
}

func (h *eventHub) drop(c *wsClient) {
	h.mu.Lock()
	h.removeClientLocked(c)
	h.mu.Unlock()
	c.close()
}

// run consumes watcher events until the watcher is closed.
func (h *eventHub) run(w *fsnotify.Watcher) {
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			h.onEvent(ev)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("file watcher: %v", err)
		}
	}
}

func (h *eventHub) onEvent(ev fsnotify.Event) {
	if tmpFileRe.MatchString(filepath.Base(ev.Name)) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	// a deleted/moved directory loses its inotify watch by itself; forget it so
	// a directory re-created with the same name gets watched again
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		delete(h.dirs, ev.Name)
	}
	// new directory inside a recursive watch: watch it too
	if ev.Has(fsnotify.Create) {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
			for c := range h.clients {
				for _, sub := range c.subs {
					if sub.recursive && sub.covers(ev.Name) && h.addDirLocked(ev.Name) == nil {
						sub.dirs = append(sub.dirs, ev.Name)
					}
				}
			}
		}
	}
	h.pending[ev.Name] = struct{}{}
	if h.timer == nil {
		h.timer = time.AfterFunc(eventCoalesce, h.flush)
	}
}

func (h *eventHub) flush() {
	h.mu.Lock()
	pending := h.pending
	h.pending = map[string]struct{}{}
	h.timer = nil
	h.mu.Unlock()

	root := h.s.ResolvePath("/")
	type change struct {
		fsPath, urlPath string
		isDir           bool
		msg             []byte
	}
	var changes []change
	for p := range pending {
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") || IsReserved(rel) {
			continue
		}
		fi, err := os.Stat(p)
		data, _ := json.Marshal(map[string]interface{}{
			"type": "change", "path": "/" + filepath.ToSlash(rel),
			"exists": err == nil, "isdir": err == nil && fi.IsDir(),
		})
		changes = append(changes, change{p, "/" + filepath.ToSlash(rel), err == nil && fi.IsDir(), data})
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range changes {
		for c := range h.clients {
			if !h.s.visible(c.user, ch.urlPath, ch.isDir) {
				continue // multi-user: only what this client's user may see
			}
			for _, sub := range c.subs {
				if sub.covers(ch.fsPath) {
					h.queueLocked(c, ch.msg)
					break
				}
			}
		}
	}
}

func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for c := range h.clients {
		h.removeClientLocked(c)
	}
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.w != nil {
		h.w.Close()
	}
}
