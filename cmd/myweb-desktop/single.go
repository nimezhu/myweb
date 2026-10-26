package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nimezhu/myweb/home"
)

// One tray per home: the running one listens on 127.0.0.1 and records the
// address in <home>/run/desktop.addr. A second start sends it "open" (or
// "hello", started at login) and exits.

func addrFile(dirs home.Dirs) string { return filepath.Join(dirs.Root, "run", "desktop.addr") }

// claim returns the listener for this tray, or handed when another one runs
// (and was asked to open the file manager if open).
func claim(dirs home.Dirs, open bool) (l net.Listener, handed bool) {
	msg := "hello"
	if open {
		msg = "open"
	}
	if b, err := os.ReadFile(addrFile(dirs)); err == nil && ask(strings.TrimSpace(string(b)), msg) {
		return nil, true
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, false // works alone
	}
	os.MkdirAll(filepath.Dir(addrFile(dirs)), 0o700)
	os.WriteFile(addrFile(dirs), []byte(l.Addr().String()+"\n"), 0o600)
	return l, false
}

// ask sends msg to the tray at addr and reports whether it answered.
func ask(addr, msg string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		return false
	}
	reply, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(reply) == "ok"
}

// serveSingle answers the other starts; on "open", calls open.
func serveSingle(l net.Listener, open func()) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			switch strings.TrimSpace(line) {
			case "open":
				open()
			case "hello":
			default:
				return
			}
			c.Write([]byte("ok\n"))
		}()
	}
}

// release removes the record if it is still this tray's.
func release(dirs home.Dirs, l net.Listener) {
	if l == nil {
		return
	}
	if b, err := os.ReadFile(addrFile(dirs)); err == nil && strings.TrimSpace(string(b)) == l.Addr().String() {
		os.Remove(addrFile(dirs))
	}
	l.Close()
}
