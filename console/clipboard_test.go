package console

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCopyStartupURL(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:8080/",
		"http://127.0.0.1:8080/?token=test-token",
		"http://127.0.0.1:8080/app/?view=files&token=test-token#folder",
	} {
		t.Run(url, func(t *testing.T) {
			m := testModel(t, 30)
			m.opts.OpenURL = url
			out, err := os.CreateTemp(t.TempDir(), "clipboard")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			// Capture the command's output without changing the actual clipboard.
			stdout := os.Stdout
			os.Stdout = out
			next, copy := m.Update(key("y"))
			os.Stdout = stdout
			if copy == nil {
				t.Fatal("y must request a copy")
			}
			m = send(next.(model), copy())
			b, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(url)) + "\x07"
			if string(b) != want {
				t.Fatalf("incorrect clipboard sequence: %q", b)
			}
			if !strings.Contains(m.flash, "terminal clipboard") {
				t.Fatalf("missing feedback: %q", m.flash)
			}
			for _, mode := range []string{"normal", "qr", "detail"} {
				m.qr, m.detail = mode == "qr", mode == "detail"
				if !strings.Contains(ansi.Strip(strings.Join(m.footerLines(), "\n")), "y copy URL") {
					t.Fatalf("missing copy shortcut in %s mode", mode)
				}
			}
			m = send(m, key("/"))
			next, copy = m.Update(key("y"))
			if copy != nil || next.(model).filter != "y" {
				t.Fatal("typing y in a filter must not copy")
			}
			out.Close()
			m = send(m, key("esc"), copyURL(url, out)())
			if !strings.Contains(m.flash, "could not copy URL") {
				t.Fatalf("missing error feedback: %q", m.flash)
			}
		})
	}
}

func TestCopyWithoutStartupURL(t *testing.T) {
	m := send(testModel(t, 30), key("y"))
	if !strings.Contains(m.flash, "nothing is served") {
		t.Fatalf("missing no-URL feedback: %q", m.flash)
	}
}
