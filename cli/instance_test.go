package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/nimezhu/myweb/home"
)

func TestInstance(t *testing.T) {
	id := "abc"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"instance":"` + id + `"}`))
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())

	dirs := home.Dirs{Root: t.TempDir()}
	file := instanceFile(dirs, "/data", "files")
	if file == instanceFile(dirs, "/data", "app:https://x/stable.json") {
		t.Fatal("another app must have its own record")
	}
	if running(file) != nil {
		t.Fatal("nothing recorded yet")
	}
	in := &instance{ID: "abc", Port: port, URL: ts.URL}
	if err := in.record(file); err != nil {
		t.Fatal(err)
	}
	if got := running(file); got == nil || got.URL != ts.URL {
		t.Fatalf("running: %+v", got)
	}
	id = "someone-else" // the port now belongs to another server
	if running(file) != nil {
		t.Fatal("a different server on the recorded port was taken for ours")
	}
	(&instance{ID: "other"}).forget(file)
	if _, err := os.Stat(file); err != nil {
		t.Fatal("forget removed another server's record")
	}
	in.forget(file)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("record not removed")
	}
}
