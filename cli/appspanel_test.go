package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nimezhu/myweb/home"
	"github.com/nimezhu/myweb/myserver"
	"github.com/nimezhu/myweb/webapp"
)

// fakeInstalled writes an installed app's state, as an install would.
func fakeInstalled(t *testing.T, dirs home.Dirs, name, manifest string) {
	pub, _, _ := webapp.GenerateKey()
	sum := sha256.Sum256([]byte(manifest))
	dir := filepath.Join(dirs.Apps(), hex.EncodeToString(sum[:6]))
	os.MkdirAll(filepath.Join(dir, "1.0.0"), 0o755)
	os.WriteFile(filepath.Join(dir, "1.0.0", "index.html"), []byte(name), 0o644)
	st, _ := json.Marshal(map[string]interface{}{"name": name, "manifest": manifest, "key": pub, "keys": []string{pub},
		"current": "1.0.0", "versions": []string{"1.0.0"}, "folders": map[string]interface{}{"1.0.0": []map[string]string{{"path": "/" + name}}}})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), st, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAppsCmd(t *testing.T) {
	dirs := home.Dirs{Root: t.TempDir()}
	fakeInstalled(t, dirs, "alpha", "https://example.com/alpha/stable.json")
	fakeInstalled(t, dirs, "beta", "https://example.com/beta/stable.json")
	srv := myserver.New(myserver.Config{Root: t.TempDir()})
	set := &appSet{dirs: dirs, root: srv.Root(), srv: srv}
	registerAppsCmd(srv, Spec{Name: "myweb"}, set)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	call := func(origin, body string) (int, []appRow) {
		req, _ := http.NewRequest("POST", ts.URL+"/cmd/apps", strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Result []appRow `json:"result"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Result
	}
	code, list := call("", `{}`)
	if code != 200 || len(list) != 2 || list[0].Name != "alpha" || list[0].Current != "1.0.0" || list[0].Access[0].Path != "/alpha" {
		t.Fatalf("list: %d %+v", code, list)
	}
	if code, list = call(ts.URL, `{"name":"alpha","allow":"/photos","write":true}`); code != 200 || len(list[0].Access) != 2 || len(list[0].Grants) != 1 {
		t.Fatalf("allow: %d %+v", code, list)
	}
	if code, list = call(ts.URL, `{"name":"alpha","deny":"/photos"}`); code != 200 || len(list[0].Grants) != 0 {
		t.Fatalf("deny: %d %+v", code, list)
	}
	// youdata.studio is a -cors site by default: it may call the API, not change apps
	if code, _ := call("https://youdata.studio", `{"name":"beta","remove":true}`); code != 403 {
		t.Errorf("remove from a -cors site: %d", code)
	}
	if code, list = call(ts.URL, `{"name":"beta","remove":true}`); code != 200 || len(list) != 1 {
		t.Fatalf("remove: %d %+v", code, list)
	}
	if code, _ := call(ts.URL, `{"name":"nope","update":true}`); code != 404 {
		t.Errorf("a missing app: %d", code)
	}
}
