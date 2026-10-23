package cli

import (
	"strings"
	"testing"
)

func TestAppFlagsParse(t *testing.T) {
	f := newAppFlags("myweb", "app remove")
	yes := f.Bool("y", false, "")
	names, err := f.parse([]string{"-home", "/x", "hello", "-y"})
	if err != nil || strings.Join(names, ",") != "hello" || *f.home != "/x" || !*yes {
		t.Fatalf("names %v home %q y %v err %v", names, *f.home, *yes, err)
	}
}

func TestMainCommands(t *testing.T) {
	dir := t.TempDir()
	for args, want := range map[string]int{
		"bogus":                        2,
		"app bogus":                    2,
		"version":                      0,
		"help":                         0,
		"app list -home " + dir:        0,
		"app versions -home " + dir:    1, // needs a name
		"app run":                      2,
		"app remove x -y -home " + dir: 1, // not installed
	} {
		if got := Main(Spec{Name: "myweb", Args: strings.Fields(args)}); got != want {
			t.Errorf("myweb %s: exit %d, want %d", args, got, want)
		}
	}
}
