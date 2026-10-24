package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorFix(t *testing.T) {
	bin := t.TempDir()
	exe := filepath.Join(bin, "myweb")
	os.WriteFile(exe, []byte("x"), 0o755)
	os.WriteFile(exe+".old.123.del", []byte("old"), 0o755)
	executable = func() (string, error) { return exe, nil }
	defer func() { executable = defaultExecutable }()
	services = func(string, string) []*service { return nil }
	defer func() { services = installedServices }()
	homeDir := filepath.Join(t.TempDir(), "h")
	spec := Spec{Name: "myweb"}

	var out bytes.Buffer
	doctorCmd(spec, []string{"-offline", "-home", homeDir}, &out)
	if !strings.Contains(out.String(), "doctor -fix fixes 1 thing") {
		t.Errorf("without -fix:\n%s", out.String())
	}
	if _, err := os.Stat(exe + ".old.123.del"); err != nil {
		t.Fatal("doctor without -fix changed something")
	}
	out.Reset()
	doctorCmd(spec, []string{"-offline", "-fix", "-home", homeDir}, &out)
	if _, err := os.Stat(exe + ".old.123.del"); !os.IsNotExist(err) {
		t.Errorf("old binary not removed:\n%s", out.String())
	}
	if fi, err := os.Stat(homeDir); err != nil || !fi.IsDir() {
		t.Errorf("home not made:\n%s", out.String())
	}
	if !strings.Contains(out.String(), ": removed") || !strings.Contains(out.String(), ": made") {
		t.Errorf("fixes not reported:\n%s", out.String())
	}
}
