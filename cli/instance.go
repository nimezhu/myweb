package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
)

// instance records a running server in <home>/run/<id>.json, so starting the
// same folder and app again opens it instead of starting a second copy.
type instance struct {
	ID      string      `json:"id"` // also reported by /cmd/status
	PID     int         `json:"pid"`
	Port    int         `json:"port"`
	URL     string      `json:"url"` // what the browser opens; may carry the token
	Root    string      `json:"root"`
	Source  string      `json:"source"` // what is served at "/"
	Version string      `json:"version,omitempty"`
	Apps    []servedApp `json:"apps,omitempty"` // other apps, on ports of their own
	Args    []string    `json:"args,omitempty"` // its command line, after the program
	Cert    string      `json:"cert,omitempty"` // HTTPS: its certificate's SHA-256 (hex)
	Started time.Time   `json:"started"`
}

// servedApp is an app a server serves on a port of its own.
type servedApp struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// instanceFile is the record for one folder and one app.
func instanceFile(dirs home.Dirs, root, source string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + source))
	return filepath.Join(dirs.Root, "run", hex.EncodeToString(sum[:6])+".json")
}

// running returns the recorded server if it is still up, else nil.
func running(file string) *instance {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var in instance
	if json.Unmarshal(b, &in) != nil || in.ID == "" || in.Port == 0 {
		return nil
	}
	// the port may have been reused since: ask the server who it is
	req, _ := http.NewRequest(http.MethodGet, in.base()+"/cmd/status", nil)
	req.Header.Set(myserver.ProbeHeader, "1") // not logged
	resp, err := in.client(time.Second).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var st struct {
		Instance string `json:"instance"`
	}
	if json.NewDecoder(resp.Body).Decode(&st) != nil || st.Instance != in.ID {
		return nil
	}
	return &in
}

// base is where this machine reaches the server: http(s)://127.0.0.1:PORT.
func (in *instance) base() string {
	scheme := "http"
	if in.Cert != "" {
		scheme = "https"
	}
	return scheme + "://127.0.0.1:" + strconv.Itoa(in.Port)
}

// client talks to the server; over HTTPS, only with its certificate.
func (in *instance) client(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if in.Cert != "" {
		c.Transport = &http.Transport{TLSClientConfig: pinnedTLS(in.Cert)}
	}
	return c
}

func newInstanceID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// record writes the file (private: the URL may carry the access token).
func (in *instance) record(file string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(in, "", "  ")
	return os.WriteFile(file, b, 0o600)
}

// forget removes the file if it is still this server's.
func (in *instance) forget(file string) {
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var cur instance
	if json.Unmarshal(b, &cur) == nil && cur.ID == in.ID {
		os.Remove(file)
	}
}
