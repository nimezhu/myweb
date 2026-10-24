package webapp

import (
	"context"
	"reflect"
	"testing"
)

func TestFolders(t *testing.T) {
	r := newRelease(t)
	r.publish(t, "1.0.0", map[string]string{"index.html": "v1"}, nil) // declares none
	dir := t.TempDir()
	in := r.installer(t, dir, nil)
	if err := in.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := in.Access(); !reflect.DeepEqual(got, []Folder{{Path: "/demo"}}) {
		t.Fatalf("default: %v", got)
	}
	r.publish(t, "1.1.0", map[string]string{"index.html": "v2"}, func(m *Manifest) {
		m.Folders = []Folder{{Path: "/demo-data"}, {Path: "/samples", ReadOnly: true}}
	})
	if err := in.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []Folder{{Path: "/demo-data"}, {Path: "/samples", ReadOnly: true}}
	if got := in.Access(); !reflect.DeepEqual(got, want) {
		t.Fatalf("declared: %v", got)
	}

	// grants: from another installer of the same app (the CLI), picked up
	// by the running one on Refresh
	cli := r.installer(t, dir, nil)
	if err := cli.Grant(Folder{Path: "photos/", ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Grant(Folder{Path: "/samples"}); err != nil { // makes it writable
		t.Fatal(err)
	}
	in.Refresh()
	want = []Folder{{Path: "/demo-data"}, {Path: "/samples"}, {Path: "/photos", ReadOnly: true}}
	if got := in.Access(); !reflect.DeepEqual(got, want) {
		t.Fatalf("with grants: %v", got)
	}
	if st := in.Status(); !reflect.DeepEqual(st.Access, want) || len(st.Grants) != 2 {
		t.Fatalf("status: %+v", st)
	}
	if err := cli.Revoke("/samples"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Revoke("/demo-data"); err == nil {
		t.Fatal("revoked a declared folder")
	}
	in.Refresh()
	if got := in.Access(); !reflect.DeepEqual(got, []Folder{{Path: "/demo-data"}, {Path: "/samples", ReadOnly: true}, {Path: "/photos", ReadOnly: true}}) {
		t.Fatalf("after revoking: %v", got)
	}
	// a one-run pin still picks up grants
	pinned := r.installer(t, dir, func(o *Options) { o.Pin = "1.0.0" })
	cli.Grant(Folder{Path: "/more"})
	pinned.Refresh()
	if got := pinned.Access(); len(got) != 3 || got[0].Path != "/demo" {
		t.Fatalf("pinned: %v", got)
	}

	for _, bad := range []string{"../x", "/a/../../b", "/.myweb", "/.myweb/history", `a\b`} {
		if err := cli.Grant(Folder{Path: bad}); err == nil {
			t.Errorf("granted %q", bad)
		}
	}
	m := Manifest{Name: "demo", Version: "1.0.0", URL: "x.zip", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	for _, f := range []string{"/", "/.myweb/x", "../up"} {
		m.Folders = []Folder{{Path: f}}
		if err := m.Validate(); err == nil {
			t.Errorf("manifest asking for %q validated", f)
		}
	}
}
