package cli

import (
	"flag"
	"strings"
	"testing"
)

func TestOverview(t *testing.T) {
	t.Setenv("MYWEB_TOKEN", "s3cret-token")
	spec := Spec{Name: "myweb"}
	var b strings.Builder
	overview(&b, spec)
	out := b.String()
	if strings.Contains(out, "s3cret-token") {
		t.Fatal("the help shows $MYWEB_TOKEN")
	}
	fs, _ := newServeFlags(spec)
	fs.VisitAll(func(f *flag.Flag) {
		if !hiddenFlags[f.Name] && !strings.Contains(out, "-"+f.Name+" ") && !strings.Contains(out, "-"+f.Name+",") && !strings.Contains(out, "-"+f.Name+"\n") {
			t.Errorf("-%s is not in the help", f.Name)
		}
	})
	for _, topic := range []string{"app", "config", "service", "completion"} {
		var b strings.Builder
		if err := helpTopic(&b, spec, topic); err != nil || !strings.Contains(b.String(), "myweb "+topic) {
			t.Errorf("topic %s: %v", topic, err)
		}
	}
	for args, want := range map[string]int{"-h": 0, "help app": 0, "app -h": 0, "app use -h": 0, "service": 0,
		"app bogus": 2, "app use -x": 2, "help nope": 2, "completion tcsh": 2} {
		if got := Main(Spec{Name: "myweb", Args: strings.Fields(args)}); got != want {
			t.Errorf("myweb %s: exit %d, want %d", args, got, want)
		}
	}
}
