package console

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nimezhu/myweb/myserver"
)

// logRotateSize: past this, the log moves to <file>.1 (replacing an older
// one) and a new file starts.
var logRotateSize int64 = 50 << 20

// accessLog appends one JSON object per line: requests and server messages.
//
//	{"time":"…","type":"request","method":"GET","path":"/f/a","status":200,"bytes":12,"ms":0.4,"remote":"…"}
//	{"time":"…","type":"log","msg":"wrote /a (12 bytes)"}
type accessLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openAccessLog(path string) (*accessLog, error) {
	a := &accessLog{path: path}
	return a, a.open()
}

func (a *accessLog) open() error {
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	a.f, a.size = f, fi.Size()
	return nil
}

func (a *accessLog) write(v interface{}) {
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return
	}
	if a.size+int64(len(line)) > logRotateSize && a.size > 0 {
		a.f.Close()
		os.Rename(a.path, a.path+".1")
		if err := a.open(); err != nil {
			a.f = nil // keep serving; logging stops
			return
		}
	}
	n, _ := a.f.Write(line)
	a.size += int64(n)
}

type logRequest struct {
	Time   time.Time `json:"time"`
	Type   string    `json:"type"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Bytes  int64     `json:"bytes"`
	Ms     float64   `json:"ms"`
	Remote string    `json:"remote"`
	Origin string    `json:"origin,omitempty"`
	Agent  string    `json:"agent,omitempty"`
	Error  string    `json:"error,omitempty"`
	User   string    `json:"user,omitempty"`
}

func (a *accessLog) request(ev myserver.RequestEvent) {
	a.write(logRequest{ev.Time, "request", ev.Method, ev.Path, ev.Status, ev.Bytes,
		float64(ev.Duration.Microseconds()) / 1000, ev.Remote, ev.Origin, ev.UserAgent, strings.TrimSpace(ev.Error), ev.User})
}

// the standard logger's own date prefix; the JSON line has a time already
var logPrefix = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(\.\d+)? `)

func (a *accessLog) message(t time.Time, msg string) {
	msg = strings.TrimSpace(logPrefix.ReplaceAllString(msg, ""))
	a.write(struct {
		Time time.Time `json:"time"`
		Type string    `json:"type"`
		Msg  string    `json:"msg"`
	}{t, "log", msg})
}

func (a *accessLog) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		a.f.Close()
		a.f = nil
	}
}

// lineWriter turns log output into one call per line.
type lineWriter struct {
	mu      sync.Mutex
	partial string
	fn      func(time.Time, string)
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := strings.Split(w.partial+string(data), "\n")
	w.partial = lines[len(lines)-1]
	now := time.Now()
	for _, l := range lines[:len(lines)-1] {
		if l = strings.TrimSpace(l); l != "" {
			w.fn(now, l)
		}
	}
	return len(data), nil
}
