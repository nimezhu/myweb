package cli

import (
	"strings"
	"testing"
)

func TestCompletions(t *testing.T) {
	t.Setenv("MYWEB_HOME", t.TempDir()) // no apps installed
	spec := Spec{Name: "myweb"}
	has := func(words string, want ...string) {
		t.Helper()
		got := " " + strings.Join(completions(spec, strings.Fields(words)), " ") + " "
		for _, w := range want {
			if !strings.Contains(got, " "+w+" ") {
				t.Errorf("%q: no %q in%s", words, w, got)
			}
		}
	}
	has("", "app", "service", "-d", "-p")
	has("app", "use", "versions", "remove")
	has("config set", "p", "d", "app")
	has("service", "install", "uninstall")
	has("service install", "-as", "-dry-run", "-app")
	has("completion", "bash", "zsh", "fish")
	if c := completions(spec, []string{"-d"}); len(c) != 0 {
		t.Errorf("-d should complete files, got %v", c)
	}
	if c := completions(spec, []string{"config", "set"}); strings.Contains(strings.Join(c, " "), "home") {
		t.Error("home can't be set in the config")
	}
	for _, sh := range []string{"bash", "zsh", "fish"} {
		var b strings.Builder
		if err := completionCmd(spec, []string{sh}, &b); err != nil || !strings.Contains(b.String(), "myweb __complete") {
			t.Errorf("%s script: %v", sh, err)
		}
	}
}
