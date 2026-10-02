package myserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
)

// MaxCmdBody limits the JSON body accepted by plugin commands.
var MaxCmdBody int64 = 1 << 20

// CmdRequest is what a plugin command receives.
type CmdRequest struct {
	Name   string
	Params json.RawMessage // request body; nil when empty
	HTTP   *http.Request
	Server *Server
	User   *User // multi-user: who is asking (nil when single-user)
}

// Can reports whether the user may do act on the URL path p (always true
// when single-user).
func (r *CmdRequest) Can(act Action, p string) bool { return r.Server.allowed(r.User, act, p) }

// Visible reports whether the user may know that p exists.
func (r *CmdRequest) Visible(p string, isDir bool) bool { return r.Server.visible(r.User, p, isDir) }

// Path checks act on the URL path p and returns its file-system path; a
// command should get every path it touches this way. Refused: a 403 error.
func (r *CmdRequest) Path(act Action, p string) (string, error) {
	if !r.Can(act, p) {
		return "", Errorf(http.StatusForbidden, "no %s permission for %s", act, path.Clean("/"+p))
	}
	return r.Server.ResolvePath(p), nil
}

// Bind decodes Params into v. An empty body leaves v unchanged.
func (r *CmdRequest) Bind(v interface{}) error {
	if len(r.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Params, v); err != nil {
		return Errorf(http.StatusBadRequest, "invalid params: %v", err)
	}
	return nil
}

// CmdFunc implements a plugin command. The returned value is sent as
// {"success":1,"result":<value>}; an error as {"success":0,"error":"..."}.
type CmdFunc func(ctx context.Context, req *CmdRequest) (interface{}, error)

// CmdError carries an HTTP status for a command failure.
type CmdError struct {
	Status int
	Msg    string
}

func (e *CmdError) Error() string { return e.Msg }

// Errorf builds a CmdError; use 4xx for caller mistakes.
func Errorf(status int, format string, a ...interface{}) error {
	return &CmdError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

type cmd struct {
	name   string
	fn     CmdFunc
	desc   string
	public bool
	writes bool
}

// CmdOption configures a registered command.
type CmdOption func(*cmd)

// Describe sets the description shown by GET /cmd/.
func Describe(d string) CmdOption { return func(c *cmd) { c.desc = d } }

// Public lets the command run without the access token.
func Public() CmdOption { return func(c *cmd) { c.public = true } }

// Writes marks a command that modifies files; it is refused in read-only mode.
func Writes() CmdOption { return func(c *cmd) { c.writes = true } }

type cmdRegistry struct {
	mu   sync.RWMutex
	cmds map[string]*cmd
}

// RegisterCmd serves fn at POST /cmd/<name>. It panics on an invalid or
// duplicate name, like http.ServeMux does.
func (s *Server) RegisterCmd(name string, fn CmdFunc, opts ...CmdOption) {
	if name == "" || strings.ContainsAny(name, "/?# ") {
		panic(fmt.Sprintf("myserver: invalid command name %q", name))
	}
	c := &cmd{name: name, fn: fn}
	for _, o := range opts {
		o(c)
	}
	s.cmds.mu.Lock()
	defer s.cmds.mu.Unlock()
	if s.cmds.cmds == nil {
		s.cmds.cmds = map[string]*cmd{}
	}
	if _, dup := s.cmds.cmds[name]; dup {
		panic("myserver: command registered twice: " + name)
	}
	s.cmds.cmds[name] = c
}

// CmdNames lists registered commands, sorted.
func (s *Server) CmdNames() []string {
	s.cmds.mu.RLock()
	defer s.cmds.mu.RUnlock()
	return s.cmds.sortedNames()
}

// sortedNames requires mu to be held.
func (reg *cmdRegistry) sortedNames() []string {
	names := make([]string, 0, len(reg.cmds))
	for n := range reg.cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

type cmdInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Public      bool   `json:"public,omitempty"`
	Writes      bool   `json:"writes,omitempty"`
}

// serveCmd handles /cmd/<name> for registered commands; built-in /cmd/...
// routes are more specific mux patterns and take precedence.
func (s *Server) serveCmd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/cmd/")
	if name == "" {
		if !s.Authorized(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		list := []cmdInfo{}
		s.cmds.mu.RLock()
		for _, n := range s.cmds.sortedNames() {
			c := s.cmds.cmds[n]
			list = append(list, cmdInfo{c.name, c.desc, c.public, c.writes})
		}
		s.cmds.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": 1, "result": list})
		return
	}

	s.cmds.mu.RLock()
	c := s.cmds.cmds[name]
	s.cmds.mu.RUnlock()
	if c == nil {
		writeError(w, http.StatusNotFound, "unknown command: "+name)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "post only")
		return
	}
	if !c.public && !s.Authorized(r) {
		writeError(w, http.StatusUnauthorized, "missing or invalid token")
		return
	}
	if c.writes && s.ReadOnly() {
		writeError(w, http.StatusForbidden, "server is in read-only mode")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxCmdBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	req := &CmdRequest{Name: name, HTTP: r, Server: s, User: UserFrom(r.Context())}
	if len(strings.TrimSpace(string(body))) > 0 {
		if !json.Valid(body) {
			writeError(w, http.StatusBadRequest, "body must be JSON")
			return
		}
		req.Params = body
	}

	result, err := c.fn(r.Context(), req)
	if err != nil {
		var ce *CmdError
		if errors.As(err, &ce) {
			writeError(w, ce.Status, ce.Msg)
			return
		}
		log.Printf("cmd %s: %v", name, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": 1, "result": result})
}
