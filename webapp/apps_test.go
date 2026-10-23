package webapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApp(t *testing.T) {
	r := newRelease(t)
	dir := t.TempDir()
	if _, err := FindApp(dir, "demo"); err == nil || !strings.Contains(err.Error(), "no apps are installed") {
		t.Fatalf("empty: %v", err)
	}
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1"}, nil)
	r.installer(t, dir, nil).check(context.Background())
	r.publish(t, "1.1.0", map[string]string{"index.html": "v1.1"}, nil)
	r.installer(t, dir, nil).check(context.Background())

	apps, err := InstalledApps(dir)
	if err != nil || len(apps) != 1 {
		t.Fatalf("list: %v %v", apps, err)
	}
	a := apps[0]
	if a.Name != "demo" || a.Current != "1.1.0" || a.Key != EncodePublicKey(r.pub) ||
		a.Manifest != r.ts.URL+"/stable.json" || strings.Join(a.Versions, ",") != "1.1.0,1.0.0" {
		t.Fatalf("app: %+v", a)
	}
	if _, err := FindApp(dir, "other"); err == nil || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("unknown name should list the installed ones: %v", err)
	}

	// -app fills in -web-from and -web-key
	f := &Flags{App: "demo"}
	in, err := f.Installer("0.1.0", dir, nil)
	if err != nil || in.Current() != "1.1.0" || f.From != a.Manifest {
		t.Fatalf("-app: %v %v", in, err)
	}
	if _, err := (&Flags{App: "demo", From: a.Manifest}).Installer("0.1.0", dir, nil); err == nil {
		t.Fatal("-app with -web-from should be refused")
	}

	// installed before keys were saved: -app asks for the key once, and a
	// successful check with -web-key saves it
	stateFile := filepath.Join(dir, apps[0].dirName(t, dir), "state.json")
	var st state
	b, _ := os.ReadFile(stateFile)
	json.Unmarshal(b, &st)
	st.Key = ""
	b, _ = json.Marshal(st)
	os.WriteFile(stateFile, b, 0o644)
	if _, err := (&Flags{App: "demo"}).Installer("0.1.0", dir, nil); err == nil || !strings.Contains(err.Error(), "-web-key") {
		t.Fatalf("no saved key: %v", err)
	}
	r.installer(t, dir, nil).check(context.Background())
	if a, _ := FindApp(dir, "demo"); a.Key != EncodePublicKey(r.pub) {
		t.Fatal("key not saved by a check")
	}
}

// dirName finds the folder an app is installed in.
func (a App) dirName(t *testing.T, dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join(dir, e.Name(), "state.json")); err == nil && strings.Contains(string(b), a.Manifest) {
			return e.Name()
		}
	}
	t.Fatal("app folder not found")
	return ""
}

func TestStatusAndNotes(t *testing.T) {
	r := newRelease(t)
	dir := t.TempDir()
	in := r.installer(t, dir, nil)
	if st := in.Status(); st.Current != "" || st.Checked != nil || !st.Updates {
		t.Fatalf("before install: %+v", st)
	}
	notes := func(n string) func(*Manifest) { return func(m *Manifest) { m.Notes = n } }
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1"}, notes("first"))
	in.check(context.Background())
	r.publish(t, "1.1.0", map[string]string{"index.html": "v1.1"}, notes("faster"))
	in.check(context.Background())
	st := in.Status()
	if st.Name != "demo" || st.Current != "1.1.0" || st.Latest != "1.1.0" || st.LatestNotes != "faster" ||
		st.Notes["1.0.0"] != "first" || st.Notes["1.1.0"] != "faster" || st.Checked == nil ||
		strings.Join(st.Installed, ",") != "1.1.0,1.0.0" {
		t.Fatalf("status: %+v", st)
	}
	if a, _ := FindApp(dir, "demo"); a.Notes["1.1.0"] != "faster" {
		t.Fatalf("app notes: %v", a.Notes)
	}
	// notes go with the versions that are pruned
	for _, v := range []string{"1.2.0", "1.3.0", "1.4.0"} {
		r.publish(t, v, map[string]string{"index.html": v}, notes("n"+v))
		in.check(context.Background())
	}
	if st := in.Status(); st.Notes["1.0.0"] != "" || st.Notes["1.4.0"] != "n1.4.0" {
		t.Fatalf("pruned notes: %v", st.Notes)
	}
	if st := r.installer(t, dir, func(o *Options) { o.NoUpdate = true }).Status(); st.Updates {
		t.Fatal("-no-update should report updates off")
	}
}

func TestUseVersion(t *testing.T) {
	r := newRelease(t)
	dir := t.TempDir()
	var updates []string
	in := r.installer(t, dir, func(o *Options) { o.OnUpdate = func(n, v string) { updates = append(updates, v) } })
	ctx := context.Background()
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0"} {
		r.publish(t, v, map[string]string{"index.html": v}, nil)
		in.check(ctx)
	}
	if serve(t, in, "/index.html") != "1.4.0" || strings.Join(in.Installed(), ",") != "1.4.0,1.3.0,1.2.0" {
		t.Fatalf("setup: %v", in.Installed())
	}
	vs, err := in.Available(ctx)
	if err != nil || len(vs.Versions) != 5 || vs.Versions[0].Version != "1.4.0" {
		t.Fatalf("available: %+v %v", vs, err)
	}

	// an older version that was pruned: downloaded, verified, pinned
	updates = nil
	if err := in.Use(ctx, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	st := in.Status()
	if serve(t, in, "/index.html") != "1.0.0" || st.Current != "1.0.0" || st.Pinned != "1.0.0" || st.Updates ||
		strings.Join(updates, ",") != "1.0.0" {
		t.Fatalf("use 1.0.0: %q %+v %v", serve(t, in, "/index.html"), st, updates)
	}
	// pinned: a newer release is not installed
	r.publish(t, "1.5.0", map[string]string{"index.html": "1.5.0"}, nil)
	in.check(ctx)
	if serve(t, in, "/index.html") != "1.0.0" || !strings.Contains(in.StatusLine(), "pinned to 1.0.0") {
		t.Fatalf("pinned: %q %s", serve(t, in, "/index.html"), in.StatusLine())
	}
	// the pin survives a restart, and another process's choice is picked up
	in2 := r.installer(t, dir, nil)
	if serve(t, in2, "/index.html") != "1.0.0" {
		t.Fatal("pin lost on restart")
	}
	if err := in2.Use(ctx, "1.4.0"); err != nil {
		t.Fatal(err)
	}
	in.check(ctx)
	if serve(t, in, "/index.html") != "1.4.0" {
		t.Fatalf("not picked up: %q", serve(t, in, "/index.html"))
	}
	// latest: follows updates again
	if err := in.Use(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	if st := in.Status(); serve(t, in, "/index.html") != "1.5.0" || st.Pinned != "" || !st.Updates {
		t.Fatalf("latest: %q %+v", serve(t, in, "/index.html"), st)
	}
	if err := in.Use(ctx, "9.9.9"); err == nil || !strings.Contains(err.Error(), "not published") {
		t.Fatalf("unknown version: %v", err)
	}
	// a tampered list is refused
	r.mu.Lock()
	r.files["stable.versions.json"] = append(r.files["stable.versions.json"], ' ')
	r.mu.Unlock()
	if _, err := in.Available(ctx); err == nil {
		t.Fatal("tampered list accepted")
	}
}

func TestKeyRotation(t *testing.T) {
	r := newRelease(t)
	ctx := context.Background()
	oldPub, oldPriv := r.pub, r.priv
	newPub, newPriv, _ := ed25519.GenerateKey(rand.Reader)
	keys := func(ks ...ed25519.PublicKey) func(*Manifest) {
		return func(m *Manifest) {
			for _, k := range ks {
				m.Keys = append(m.Keys, EncodePublicKey(k))
			}
		}
	}
	dir, offline := t.TempDir(), t.TempDir()
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1"}, nil)
	r.installer(t, dir, nil).check(ctx)
	r.installer(t, offline, nil).check(ctx) // a second user, offline from now until the rotation is done

	// rotate: releases are signed with both keys and name both
	r.sign = []ed25519.PrivateKey{newPriv, oldPriv}
	r.publish(t, "1.1.0", map[string]string{"index.html": "v1.1"}, keys(newPub, oldPub))
	r.installer(t, dir, nil).check(ctx)
	a, _ := FindApp(dir, "demo")
	if a.Current != "1.1.0" || a.Key != EncodePublicKey(newPub) {
		t.Fatalf("after rotate: %+v", a)
	}
	// finish: the new key only; -app (which uses the saved key) keeps working
	r.sign = []ed25519.PrivateKey{newPriv}
	r.publish(t, "1.2.0", map[string]string{"index.html": "v1.2"}, keys(newPub))
	in, err := (&Flags{App: "demo"}).Installer("0.1.0", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Update(ctx); err != nil || in.Current() != "1.2.0" {
		t.Fatalf("after finish: %v %s", err, in.Current())
	}
	// the old key is no longer trusted
	r.sign = []ed25519.PrivateKey{oldPriv}
	r.publish(t, "1.3.0", map[string]string{"index.html": "evil"}, nil)
	in, _ = (&Flags{App: "demo"}).Installer("0.1.0", dir, nil)
	if err := in.Update(ctx); err == nil || in.Current() != "1.2.0" {
		t.Fatalf("a release signed with the retired key was accepted: %v %s", err, in.Current())
	}
	// even when the retired key is given explicitly (e.g. an old -web-key)
	if err := r.installer(t, dir, func(o *Options) { o.PublicKey = oldPub }).Update(ctx); err == nil {
		t.Fatal("a retired -web-key was trusted")
	}
	// the user who missed the rotation can't verify the new key's releases:
	// they need the new public key once (-web-key)
	r.sign = []ed25519.PrivateKey{newPriv}
	r.publish(t, "1.4.0", map[string]string{"index.html": "v1.4"}, keys(newPub))
	if err := r.installer(t, offline, nil).Update(ctx); err == nil {
		t.Fatal("verified without knowing the new key")
	}
	if err := r.installer(t, offline, func(o *Options) { o.PublicKey = newPub }).Update(ctx); err != nil {
		t.Fatalf("with the new key: %v", err)
	}
}
