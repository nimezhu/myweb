package cli

import (
	"strings"
	"testing"
)

func TestPickServer(t *testing.T) {
	list := []server{
		{instance: &instance{PID: 100, Port: 8433, Root: "/data/a"}, Serve: "files"},
		{instance: &instance{PID: 200, Port: 8434, Root: "/data/b"}, App: "cellome", Serve: "app cellome"},
		{instance: &instance{PID: 300, Port: 8435, Root: "/data/b"}, Serve: "files"},
	}
	for arg, pid := range map[string]int{"1": 100, "3": 300, "200": 200, "8435": 300, "cellome": 200, "/data/a": 100} {
		s, err := pickServer("myweb", list, []string{arg}, "stop")
		if err != nil || s.PID != pid {
			t.Errorf("%s: pid %v, %v; want %d", arg, s.instance, err, pid)
		}
	}
	for arg, msg := range map[string]string{"/data/b": "2 servers match", "9": "no running server 9", "nope": "no running server for"} {
		if _, err := pickServer("myweb", list, []string{arg}, "stop"); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want %q", arg, err, msg)
		}
	}
	if _, err := pickServer("myweb", list, nil, "stop"); err == nil || !strings.Contains(err.Error(), "say which") {
		t.Errorf("several, none named: %v", err)
	}
	if s, err := pickServer("myweb", list[:1], nil, "open"); err != nil || s.PID != 100 {
		t.Errorf("the only one: %v", err)
	}
	if _, err := pickServer("myweb", nil, nil, "open"); err == nil || !strings.Contains(err.Error(), "no myweb is running") {
		t.Errorf("none: %v", err)
	}
}

func TestServerAddress(t *testing.T) {
	s := server{instance: &instance{Port: 9000, URL: "http://127.0.0.1:9000/?token=secret"}}
	if s.address() != "http://127.0.0.1:9000/" || s.token() != "secret" {
		t.Errorf("address %q token %q", s.address(), s.token())
	}
	s = server{instance: &instance{Port: 9000}}
	if s.address() != "http://127.0.0.1:9000/_myweb/" || s.token() != "" {
		t.Errorf("no URL: address %q token %q", s.address(), s.token())
	}
}
