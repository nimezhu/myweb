package console

import (
	"context"
	"strings"
	"testing"
)

type fakeCatalog struct {
	items     []CatalogItem
	installed []string
}

func (f *fakeCatalog) List(context.Context, bool) ([]CatalogItem, error) {
	return append([]CatalogItem(nil), f.items...), nil
}
func (f *fakeCatalog) Install(_ context.Context, name string) error {
	f.installed = append(f.installed, name)
	for i := range f.items {
		if f.items[i].Name == name {
			f.items[i].Installed = "1.0.0"
		}
	}
	return nil
}
func (f *fakeCatalog) Open(name string) (string, error) { return "http://127.0.0.1:8434/", nil }

func TestCatalogView(t *testing.T) {
	var opened []string
	openURL = func(u string) error { opened = append(opened, u); return nil }
	defer func() { openURL = OpenBrowser }()

	m := testModel(t, 30)
	cat := &fakeCatalog{items: []CatalogItem{
		{Name: "rx-graphic", Title: "RX Graphic", Installed: "0.0.1", URL: "http://127.0.0.1:8434/", Tags: []string{"graphics"}},
		{Name: "notes", Title: "Notes", Description: "Markdown notes in your data folder"},
	}}
	m.opts.Catalog = cat
	if !strings.Contains(m.View(), "apps") {
		t.Fatal("no 'i apps' hint")
	}
	step := func(k string) {
		next, cmd := m.Update(key(k))
		m = run(next.(model), cmd)
	}
	step("i")
	v := m.View()
	for _, want := range []string{"Apps", "rx-graphic", "served", "notes", "not installed", "graphics", "install / open"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	step("down")
	if !strings.Contains(m.View(), "Markdown notes") {
		t.Error("selected app's description not shown")
	}
	step("enter") // install
	if len(cat.installed) != 1 || cat.installed[0] != "notes" || !strings.Contains(m.flash, "installed") {
		t.Fatalf("install: %v %q", cat.installed, m.flash)
	}
	step("up")
	step("enter") // open
	if len(opened) != 1 || opened[0] != "http://127.0.0.1:8434/" {
		t.Fatalf("open: %v", opened)
	}
	step("esc")
	if m.catView {
		t.Error("esc didn't leave the apps view")
	}
}
