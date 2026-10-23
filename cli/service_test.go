package cli

import (
	"os"
	"path/filepath"
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
