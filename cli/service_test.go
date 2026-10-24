package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/home"
)

func TestServiceDefinitions(t *testing.T) {
	dirs := home.Dirs{Root: "/h/.myweb"}
	args := []string{"-app=hello", "-d=/data/my files"}
	linux := newService("linux", "myweb", "myweb", "/usr/bin/myweb", dirs, args).definition()
	if !strings.Contains(linux, `ExecStart=/usr/bin/myweb -no-tui -no-open -home /h/.myweb -app=hello "-d=/data/my files"`) {
		t.Errorf("linux:\n%s", linux)
	}
	mac := newService("darwin", "myweb", "myweb-hello", "/usr/local/bin/myweb", dirs, args).definition()
	for _, want := range []string{"<string>com.github.nimezhu.myweb-hello</string>", "<string>-d=/data/my files</string>",
		"<string>-log</string>", "<string>/h/.myweb/logs/myweb-hello.log</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(mac, want) {
			t.Errorf("darwin: no %q in\n%s", want, mac)
		}
	}
	win := newService("windows", "myweb", "myweb", `C:\Program Files\myweb\myweb.exe`, home.Dirs{Root: `C:\Users\a\.myweb`}, args).definition()
	if !strings.Contains(win, `Run """C:\Program Files\myweb\myweb.exe"" -no-tui -no-open -home C:\Users\a\.myweb -app=hello ""-d=/data/my files"" -log`) {
		t.Errorf("windows:\n%s", win)
	}
	if got := systemdQuote(`50%"x`); got != `"50%%\"x"` {
		t.Errorf("systemdQuote: %s", got)
	}
	if got := windowsQuote(`a b\`); got != `"a b\\"` {
		t.Errorf("windowsQuote: %s", got)
	}
}

func TestServiceInstallLinux(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	var ran []string
	old := runCmd
	defer func() { runCmd = old }()
	runCmd = func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return "", nil
	}
	s := newService("linux", "myweb", "myweb", "/usr/bin/myweb", home.Dirs{Root: t.TempDir()}, nil)
	if err := s.install(false); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(cfg, "systemd", "user", "myweb.service")
	if b, err := os.ReadFile(unit); err != nil || !strings.Contains(string(b), "ExecStart=/usr/bin/myweb") {
		t.Fatalf("unit: %v", err)
	}
	if !strings.Contains(strings.Join(ran, "\n"), "systemctl --user enable myweb.service") {
		t.Fatalf("commands: %v", ran)
	}
	if err := s.uninstall(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("unit not removed")
	}
}

func TestServiceArgs(t *testing.T) {
	spec := Spec{Name: "myweb"}
	got, err := checkServeArgs(spec, []string{"-d", ".", "-app", "hello"})
	wd, _ := os.Getwd()
	if err != nil || strings.Join(got, " ") != "-app=hello -d="+wd {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range [][]string{{"-token", "x"}, {"-apps"}, {"extra"}, {"-p", "abc"}} {
		if _, err := checkServeArgs(spec, bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestServiceRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	var ran []string
	old := runCmd
	defer func() { runCmd = old }()
	runCmd = func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "is-active" {
			if args[2] == "myweb.service" {
				return "active", nil
			}
			return "inactive", nil
		}
		return "", nil
	}
	dirs := home.Dirs{Root: t.TempDir()}
	write := func(s *service) {
		f, _ := s.file()
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte(s.definition()), 0o644)
	}
	write(newService("linux", "myweb", "myweb", "/usr/bin/myweb", dirs, nil))
	write(newService("linux", "myweb", "myweb-off", "/usr/bin/myweb", dirs, nil))
	write(newService("linux", "myweb", "other", "/opt/myweb", dirs, nil))
	got := installedServices("linux", "/usr/bin/myweb")
	if len(got) != 2 || got[0].name+","+got[1].name != "myweb-off,myweb" {
		t.Fatalf("services: %+v", got)
	}
	for _, s := range got {
		ok, err := s.restart(nil)
		if err != nil || ok != (s.name == "myweb") {
			t.Errorf("%s: %v %v", s.name, ok, err)
		}
	}
	if !strings.Contains(strings.Join(ran, "\n"), "systemctl --user restart myweb.service") || strings.Contains(strings.Join(ran, "\n"), "restart myweb-off") {
		t.Errorf("ran: %v", ran)
	}

	// Windows: the server the service started is stopped, then started again
	exe := `C:\myweb\myweb.exe`
	svc := newService("windows", "myweb", "myweb", exe, dirs, []string{"-app=hello"})
	write(svc)
	stopped := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cmd/stopServer" {
			stopped++
		}
	}))
	defer ts.Close()
	port, _ := strconv.Atoi(ts.URL[strings.LastIndex(ts.URL, ":")+1:])
	mine := server{instance: &instance{Port: port, Args: svc.args}, file: filepath.Join(t.TempDir(), "gone.json")}
	other := server{instance: &instance{Port: port, Args: []string{"-d=/x"}}, file: mine.file}
	found := installedServices("windows", exe)
	if len(found) != 1 {
		t.Fatalf("windows services: %+v", found)
	}
	ran = nil
	if ok, err := found[0].restart([]server{other, mine}); !ok || err != nil || stopped != 1 || len(ran) != 1 || !strings.HasPrefix(ran[0], "wscript ") {
		t.Errorf("windows: %v %v stopped %d ran %v", ok, err, stopped, ran)
	}
	if ok, _ := found[0].restart([]server{other}); ok {
		t.Error("windows: restarted a service that wasn't running")
	}
}
