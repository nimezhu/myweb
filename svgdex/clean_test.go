package svgdex

import (
	"strings"
	"testing"
)

func TestCleanStripsWhatIsNotSafe(t *testing.T) {
	in := `<?xml version="1.0"?>
<!DOCTYPE svg [ <!ENTITY x "boom"> ]>
<!-- a comment -->
<svg width="24px" height="16" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" inkscape:version="1.0">
  <script>alert(1)</script>
  <metadata><rdf:RDF/></metadata>
  <foreignObject><div xmlns="http://www.w3.org/1999/xhtml">hi</div></foreignObject>
  <title>Nerve cell</title>
  <a href="javascript:alert(1)"><rect width="3" height="3" onclick="alert(1)" fill="#F00"/></a>
  <image href="https://evil.example/x.png" width="1" height="1"/>
  <use xlink:href="#r"/>
  <text x="1" y="2"> keep  spaces </text>
</svg>`
	out, info, err := Clean([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"<script", "alert", "foreignObject", "metadata", "inkscape", "DOCTYPE", "<?xml", "comment", "evil.example", "onclick"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q left in: %s", bad, s)
		}
	}
	for _, good := range []string{`xmlns="http://www.w3.org/2000/svg"`, `xmlns:xlink="http://www.w3.org/1999/xlink"`, `viewBox="0 0 24 16"`, `<use xlink:href="#r"/>`, `> keep  spaces </text>`, `<title>Nerve cell</title>`} {
		if !strings.Contains(s, good) {
			t.Errorf("missing %q in: %s", good, s)
		}
	}
	if info.W != 24 || info.H != 16 {
		t.Errorf("size %vx%v", info.W, info.H)
	}
	if strings.Join(info.Words, " ") != "nerve cell" {
		t.Errorf("words %v", info.Words)
	}
	if !info.Mono || info.Colors != 1 {
		t.Errorf("one colour: mono=%v colors=%d", info.Mono, info.Colors)
	}
}

func TestCleanColoursAndViewBox(t *testing.T) {
	_, info, err := Clean([]byte(`<svg viewBox="0 0 10 20"><path fill="black"/><path style="fill:#000;stroke: #ff0000"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mono || info.Colors != 2 || info.W != 10 || info.H != 20 {
		t.Errorf("got %+v", info)
	}
	_, info, _ = Clean([]byte(`<svg viewBox="0 0 1 1"><path fill="currentColor"/><path/></svg>`))
	if !info.Mono {
		t.Errorf("currentColor / no fill is mono: %+v", info)
	}
	_, info, _ = Clean([]byte(`<svg viewBox="0 0 1 1"><title>Insulin pathway</title><g><desc>Mitochondria</desc><title>Octagon</title></g></svg>`))
	if strings.Join(info.Words, " ") != "insulin pathway" {
		t.Errorf("only the document's own title / desc: %v", info.Words)
	}
	_, info, _ = Clean([]byte(`<svg viewBox="0 0 1 1"><linearGradient id="g"/><path fill="url(#g)"/></svg>`))
	if info.Mono {
		t.Errorf("a gradient is colour: %+v", info)
	}
	if _, _, err := Clean([]byte(`<html><body/></html>`)); err == nil {
		t.Error("a non-SVG root is refused")
	}
	if _, _, err := Clean([]byte(`not xml`)); err == nil {
		t.Error("garbage is refused")
	}
}

func TestWordsAndNames(t *testing.T) {
	if got := strings.Join(Words("ArrowLeft2 nerve_cell-icon"), " "); got != "arrow left 2 nerve cell icon" {
		t.Errorf("Words: %q", got)
	}
	if got := NameFromKey("cells/nerve_cell-02.svg"); got != "nerve cell 02" {
		t.Errorf("NameFromKey: %q", got)
	}
	if SetID("Bio Icons!") != "bio-icons" {
		t.Error("SetID")
	}
}
