package cli

import (
	"bufio"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// -tunnel: reach the server from anywhere through a Cloudflare quick tunnel
// (cloudflared, no account), at a random https://….trycloudflare.com
// address that lasts while the server runs. The token is always on then.

// tunnelCommand runs the tunnel; replaced in tests.
var tunnelCommand = "cloudflared"

const cloudflaredHelp = "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/"

var tunnelURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type tunnel struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

// startTunnel tunnels to target (the server's own address). onURL gets the
// public address once known, and "" if the tunnel ends.
func startTunnel(target string, onURL func(string)) (*tunnel, error) {
	bin, err := exec.LookPath(tunnelCommand)
	if err != nil {
		return nil, errors.New("-tunnel needs cloudflared on PATH (brew install cloudflared, or " + cloudflaredHelp + ")")
	}
	args := []string{"tunnel", "--no-autoupdate", "--url", target}
	if strings.HasPrefix(target, "https:") {
		args = append(args, "--no-tls-verify") // our own certificate, on 127.0.0.1
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.StderrPipe() // cloudflared logs to stderr
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &tunnel{cmd: cmd, done: make(chan struct{})}
	go func() {
		found := false
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line := sc.Text()
			if u := tunnelURL.FindString(line); u != "" && !found {
				found = true
				onURL(u)
			}
			if strings.Contains(line, " ERR ") {
				log.Printf("tunnel: %s", line)
			}
		}
		cmd.Wait()
		close(t.done)
		onURL("")
		log.Printf("tunnel: ended")
	}()
	return t, nil
}

// stop ends the tunnel and waits a little for it.
func (t *tunnel) stop() {
	t.once.Do(func() {
		if runtime.GOOS == "windows" {
			t.cmd.Process.Kill()
		} else {
			t.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-t.done:
		case <-time.After(3 * time.Second):
			t.cmd.Process.Kill()
		}
	})
}
