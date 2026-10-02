package svgdex

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Info is what Clean learns about an SVG.
type Info struct {
	W, H   float64  // the viewBox size (0 when it has none and no width / height)
	Colors int      // distinct paint colours (fill / stroke / stop-color); a gradient counts as many
	Mono   bool     // one colour (or none given: black) — recolourable as a whole
	Words  []string // from its own <title> / <desc> (the root's, not its shapes')
}

// MaxSVGBytes is the largest SVG Clean accepts.
const MaxSVGBytes = 5 << 20

const svgNS = "http://www.w3.org/2000/svg"

// Clean makes an SVG safe to serve and insert, and measures it. Token by token (the text is kept, not re-encoded):
//   - out: scripts, foreignObject (HTML inside), event attributes (on…), links to anything but a fragment (#id) or an
//     embedded image (data:image/…), the XML declaration, DOCTYPE (no entities) and comments, editor clutter
//     (Inkscape / Sodipodi elements and attributes, <metadata>), whitespace between elements;
//   - the root must be <svg>: it gets the SVG namespace (and xlink's when used) and a viewBox from its width / height
//     when it has none.
func Clean(src []byte) ([]byte, Info, error) {
	var info Info
	if len(src) > MaxSVGBytes {
		return nil, info, fmt.Errorf("svgdex: SVG larger than %d MB", MaxSVGBytes>>20)
	}
	d := xml.NewDecoder(bytes.NewReader(src))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	var out bytes.Buffer
	var stack []string // open elements (qualified names)
	skip := 0          // >0 while inside a dropped element
	open := false      // a start tag written without its ">" yet (it may self-close)
	inWords := 0       // inside <title> / <desc>
	sawRoot := false
	usesXlink := false
	colors := map[string]bool{}
	gradient := false
	var words []string
	closeOpen := func() {
		if open {
			out.WriteByte('>')
			open = false
		}
	}
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, info, fmt.Errorf("svgdex: not well-formed SVG: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := qname(t.Name)
			if skip > 0 || dropElement(t.Name) {
				skip++
				continue
			}
			if !sawRoot {
				if t.Name.Local != "svg" {
					return nil, info, errors.New("svgdex: the root element is not <svg>")
				}
				sawRoot = true
				t.Attr, info.W, info.H = rootAttrs(t.Attr)
			}
			closeOpen()
			out.WriteByte('<')
			out.WriteString(name)
			for _, a := range t.Attr {
				if dropAttr(a) {
					continue
				}
				if a.Name.Space == "xlink" || (a.Name.Space == "xmlns" && a.Name.Local == "xlink") {
					usesXlink = true
				}
				noteColors(a, colors, &gradient)
				out.WriteByte(' ')
				out.WriteString(qname(a.Name))
				out.WriteString(`="`)
				xml.EscapeText(&out, []byte(a.Value))
				out.WriteByte('"')
			}
			open = true
			// The document's OWN title / desc (children of the root) name it; a shape's (a diagram labels every
			// symbol: "Octagon", "Mitochondria") would make it match everything.
			if (t.Name.Local == "title" || t.Name.Local == "desc") && len(stack) == 1 {
				inWords++
			}
			stack = append(stack, name)
			// a gradient, pattern or embedded picture: many colours — not recolourable as a whole
			if t.Name.Local == "linearGradient" || t.Name.Local == "radialGradient" || t.Name.Local == "pattern" {
				gradient = true
			}
			if t.Name.Local == "image" { // only one that keeps its picture (an embedded one) draws anything
				for _, a := range t.Attr {
					if a.Name.Local == "href" && strings.HasPrefix(strings.TrimSpace(a.Value), "data:image/") {
						gradient = true
					}
				}
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				continue
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if (t.Name.Local == "title" || t.Name.Local == "desc") && len(stack) == 1 && inWords > 0 {
				inWords--
			}
			if open {
				out.WriteString("/>")
				open = false
			} else {
				out.WriteString("</" + name + ">")
			}
		case xml.CharData:
			if skip > 0 || len(stack) == 0 {
				continue
			}
			if inWords > 0 {
				words = append(words, string(t))
			}
			if len(bytes.TrimSpace(t)) == 0 && !inText(stack) {
				continue // whitespace between elements
			}
			closeOpen()
			xml.EscapeText(&out, t)
		}
		// xml.Comment, xml.ProcInst, xml.Directive: dropped
	}
	if !sawRoot {
		return nil, info, errors.New("svgdex: no <svg> element")
	}
	for len(stack) > 0 { // a truncated file: close what is open
		closeOpen()
		out.WriteString("</" + stack[len(stack)-1] + ">")
		stack = stack[:len(stack)-1]
	}
	res := out.Bytes()
	// The namespaces a standalone SVG file needs (an unprefixed root; a prefixed <svg:svg> declares its own).
	plainRoot := len(res) > 4 && string(res[:4]) == "<svg" && (res[4] == ' ' || res[4] == '>' || res[4] == '/')
	head := func() []byte { return res[:bytes.IndexByte(res, '>')+1] }
	if plainRoot && !bytes.Contains(head(), []byte(` xmlns="`)) {
		res = append([]byte(`<svg xmlns="`+svgNS+`"`), res[4:]...)
	}
	if plainRoot && usesXlink && !bytes.Contains(head(), []byte(`xmlns:xlink=`)) {
		res = append([]byte(`<svg xmlns:xlink="http://www.w3.org/1999/xlink"`), res[4:]...)
	}
	delete(colors, "")
	info.Colors = len(colors)
	if gradient {
		info.Colors += 2
	}
	info.Mono = !gradient && len(colors) <= 1
	info.Words = Words(strings.Join(words, " "))
	return res, info, nil
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

var editorNS = map[string]bool{"inkscape": true, "sodipodi": true, "sketch": true, "serif": true, "dc": true, "cc": true, "rdf": true}

func dropElement(n xml.Name) bool {
	switch n.Local {
	case "script", "foreignObject", "metadata":
		return true
	}
	return editorNS[n.Space]
}

func dropAttr(a xml.Attr) bool {
	l := strings.ToLower(a.Name.Local)
	if strings.HasPrefix(l, "on") || editorNS[a.Name.Space] || (a.Name.Space == "xmlns" && editorNS[a.Name.Local]) {
		return true
	}
	if l == "href" {
		v := strings.TrimSpace(a.Value)
		return !(strings.HasPrefix(v, "#") || strings.HasPrefix(v, "data:image/"))
	}
	if strings.Contains(strings.ToLower(a.Value), "javascript:") {
		return true
	}
	return false
}

var num = regexp.MustCompile(`^\s*([0-9.]+)\s*(px)?\s*$`)

// rootAttrs: the root's size from its viewBox — added from width / height when missing.
func rootAttrs(attrs []xml.Attr) ([]xml.Attr, float64, float64) {
	var vb, w, h string
	for _, a := range attrs {
		switch a.Name.Local {
		case "viewBox":
			vb = a.Value
		case "width":
			w = a.Value
		case "height":
			h = a.Value
		}
	}
	if f := strings.Fields(strings.ReplaceAll(vb, ",", " ")); len(f) == 4 {
		vw, _ := strconv.ParseFloat(f[2], 64)
		vh, _ := strconv.ParseFloat(f[3], 64)
		return attrs, vw, vh
	}
	mw, mh := num.FindStringSubmatch(w), num.FindStringSubmatch(h)
	if mw == nil || mh == nil {
		return attrs, 0, 0
	}
	vw, _ := strconv.ParseFloat(mw[1], 64)
	vh, _ := strconv.ParseFloat(mh[1], 64)
	if vw <= 0 || vh <= 0 {
		return attrs, 0, 0
	}
	return append(attrs, xml.Attr{Name: xml.Name{Local: "viewBox"}, Value: fmt.Sprintf("0 0 %g %g", vw, vh)}), vw, vh
}

var styleColor = regexp.MustCompile(`(?i)(?:^|;)\s*(fill|stroke|stop-color)\s*:\s*([^;]+)`)

// noteColors records the paint colours an attribute sets (fill / stroke / stop-color, or inside style="…").
func noteColors(a xml.Attr, colors map[string]bool, gradient *bool) {
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important")))
		switch {
		case v == "" || v == "none" || v == "transparent" || v == "currentcolor" || v == "inherit":
		case strings.HasPrefix(v, "url("):
			*gradient = true
		default:
			colors[normColor(v)] = true
		}
	}
	switch a.Name.Local {
	case "fill", "stroke", "stop-color":
		add(a.Value)
	case "style":
		for _, m := range styleColor.FindAllStringSubmatch(a.Value, -1) {
			add(m[2])
		}
	}
}

// normColor: #abc → #aabbcc; black / white names to hex (so "black" and "#000" are one colour).
func normColor(v string) string {
	switch v {
	case "black":
		return "#000000"
	case "white":
		return "#ffffff"
	}
	if len(v) == 4 && v[0] == '#' {
		return "#" + string([]byte{v[1], v[1], v[2], v[2], v[3], v[3]})
	}
	return v
}

// inText: whitespace matters inside text.
func inText(stack []string) bool {
	for _, n := range stack {
		switch n {
		case "text", "tspan", "textPath", "title", "desc", "style":
			return true
		}
	}
	return false
}
