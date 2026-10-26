package myserver

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// halves is a w×h image, its left half red and its right half blue; alpha
// below 255 makes it transparent.
func halves(w, h int, alpha uint8) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{0, 0, 255, alpha}
			if x < w/2 {
				c = color.NRGBA{255, 0, 0, alpha}
			}
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

func jpegBytes(t *testing.T, m image.Image, orientation int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if orientation == 0 {
		return b
	}
	// an APP1 EXIF segment right after SOI: II, IFD0 with one entry, Orientation
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00")
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:], 3) // SHORT
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], uint16(orientation))
	tiff = append(append(tiff, entry...), 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	return append(append(append([]byte{0xff, 0xd8}, app1...), seg...), b[2:]...)
}

func pngBytes(t *testing.T, m image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func getThumb(t *testing.T, url string, hdr http.Header) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestThumb(t *testing.T) {
	x := newVersionServer(t, Config{})
	os.WriteFile(filepath.Join(x.root, "wide.jpg"), jpegBytes(t, halves(800, 400, 255), 0), 0o644)

	resp, b := getThumb(t, x.url+"/thumb/wide.jpg?w=200", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	m, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil || m.Bounds().Dx() != 256 || m.Bounds().Dy() != 128 {
		t.Fatalf("w=200 rounds up to 256 and keeps the shape: %v %v", m.Bounds(), err)
	}
	cached, _ := filepath.Glob(filepath.Join(x.root, ".myweb", "thumbs", "*", "*.jpg"))
	if len(cached) != 1 {
		t.Fatalf("kept: %v", cached)
	}

	etag := resp.Header.Get("ETag")
	if r, _ := getThumb(t, x.url+"/thumb/wide.jpg?w=200", http.Header{"If-None-Match": {etag}}); r.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", r.StatusCode)
	}
	if r, b2 := getThumb(t, x.url+"/thumb/wide.jpg?w=256", nil); r.StatusCode != 200 || !bytes.Equal(b, b2) || r.Header.Get("ETag") != etag {
		t.Fatal("the same size from the cache")
	}
	if r, _ := getThumb(t, x.url+"/thumb/wide.jpg?w=200&v=1", nil); r.Header.Get("Cache-Control") != "private, max-age=604800" {
		t.Fatalf("v= may be cached: %q", r.Header.Get("Cache-Control"))
	}

	// a changed file has a new thumbnail
	later := time.Now().Add(time.Minute)
	os.WriteFile(filepath.Join(x.root, "wide.jpg"), jpegBytes(t, halves(600, 400, 255), 0), 0o644)
	os.Chtimes(filepath.Join(x.root, "wide.jpg"), later, later)
	if r, _ := getThumb(t, x.url+"/thumb/wide.jpg?w=200", nil); r.Header.Get("ETag") == etag {
		t.Fatal("same ETag after a change")
	}
}

func TestThumbKinds(t *testing.T) {
	x := newVersionServer(t, Config{})
	small := pngBytes(t, halves(40, 30, 255))
	writeFiles(t, x.root, map[string]string{
		"small.png":    string(small),
		"clear.png":    string(pngBytes(t, halves(600, 600, 128))),
		"turned.jpg":   string(jpegBytes(t, halves(400, 200, 255), 6)), // shown turned 90° clockwise
		"notes.txt":    "not an image",
		"broken.png":   "\x89PNG\r\n\x1a\nnope",
		".myweb/x.jpg": string(jpegBytes(t, halves(400, 200, 255), 0)),
	})

	if r, b := getThumb(t, x.url+"/thumb/small.png", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || !bytes.Equal(b, small) {
		t.Fatalf("a small image is sent as it is: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if kept, _ := filepath.Glob(filepath.Join(x.root, ".myweb", "thumbs", "*", "*")); len(kept) != 0 {
		t.Fatalf("nothing to keep for it: %v", kept)
	}

	r, b := getThumb(t, x.url+"/thumb/clear.png?w=100", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("transparent: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if m, err := png.Decode(bytes.NewReader(b)); err != nil || m.Bounds().Dx() != 128 {
		t.Fatalf("transparent stays PNG: %v", err)
	} else if _, _, _, a := m.At(10, 10).RGBA(); a == 0xffff {
		t.Fatal("transparency lost")
	}

	r, b = getThumb(t, x.url+"/thumb/turned.jpg?w=256", nil)
	m, err := jpeg.Decode(bytes.NewReader(b))
	if r.StatusCode != 200 || err != nil || m.Bounds().Dx() != 128 || m.Bounds().Dy() != 256 {
		t.Fatalf("EXIF orientation 6 turns it upright: %d %v", r.StatusCode, err)
	}
	// turned clockwise, the red left half is on top
	if rr, _, bb, _ := m.At(64, 20).RGBA(); rr < 0xc000 || bb > 0x4000 {
		t.Fatalf("top is not red: %v", m.At(64, 20))
	}
	if rr, _, bb, _ := m.At(64, 236).RGBA(); bb < 0xc000 || rr > 0x4000 {
		t.Fatalf("bottom is not blue: %v", m.At(64, 236))
	}

	for p, want := range map[string]int{
		"/thumb/notes.txt":    http.StatusUnsupportedMediaType,
		"/thumb/broken.png":   http.StatusUnsupportedMediaType, // its header is unreadable
		"/thumb/missing.jpg":  http.StatusNotFound,
		"/thumb/":             http.StatusNotFound,
		"/thumb/.myweb/x.jpg": http.StatusNotFound,
	} {
		if r, _ := getThumb(t, x.url+p, nil); r.StatusCode != want {
			t.Errorf("%s: %d, want %d", p, r.StatusCode, want)
		}
	}
}

func TestThumbConcurrent(t *testing.T) {
	x := newVersionServer(t, Config{})
	os.WriteFile(filepath.Join(x.root, "a.jpg"), jpegBytes(t, halves(1200, 900, 255), 0), 0o644)
	var wg sync.WaitGroup
	got := make([][]byte, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, b := getThumb(t, x.url+"/thumb/a.jpg", nil)
			if r.StatusCode == 200 {
				got[i] = b
			}
		}(i)
	}
	wg.Wait()
	for i := range got {
		if got[i] == nil || !bytes.Equal(got[i], got[0]) {
			t.Fatalf("request %d differs", i)
		}
	}
}

func TestThumbAccess(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"users/alice/a.jpg": string(jpegBytes(t, halves(600, 400, 255), 0))})
	s := New(Config{Root: root, Access: newTestAccess()})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	for who, want := range map[string]int{"alice": 200, "bob": 404, "root": 200, "": 401} {
		h := http.Header{}
		if who != "" {
			h.Set("Authorization", "Bearer tok-"+who)
		}
		if r, _ := getThumb(t, ts.URL+"/thumb/users/alice/a.jpg", h); r.StatusCode != want {
			t.Errorf("%q: %d, want %d", who, r.StatusCode, want)
		}
	}
}

func TestThumbSide(t *testing.T) {
	for q, want := range map[string]int{"": 256, "x": 256, "-3": 256, "1": 64, "64": 64, "65": 128, "300": 512, "5000": 1024} {
		if got := thumbSide(q); got != want {
			t.Errorf("thumbSide(%q) = %d, want %d", q, got, want)
		}
	}
}

// Big images are averaged down first (shrink): its kinds keep their colours.
func TestThumbShrink(t *testing.T) {
	x := newVersionServer(t, Config{})
	gray := image.NewGray(image.Rect(0, 0, 2000, 1000))
	for i := range gray.Pix {
		gray.Pix[i] = 200
	}
	writeFiles(t, x.root, map[string]string{
		"big.jpg":  string(jpegBytes(t, halves(2000, 1000, 255), 0)),
		"big.png":  string(pngBytes(t, halves(2000, 1000, 255))), // opaque NRGBA
		"gray.png": string(pngBytes(t, gray)),
	})
	near := func(c color.Color, r, g, b int) bool {
		cr, cg, cb, _ := c.RGBA()
		d := func(a uint32, b int) bool { v := int(a >> 8); return v >= b-24 && v <= b+24 }
		return d(cr, r) && d(cg, g) && d(cb, b)
	}
	for _, name := range []string{"big.jpg", "big.png"} {
		r, b := getThumb(t, x.url+"/thumb/"+name, nil)
		m, _, err := image.Decode(bytes.NewReader(b))
		if r.StatusCode != 200 || err != nil || m.Bounds().Dx() != 256 || m.Bounds().Dy() != 128 {
			t.Fatalf("%s: %d %v", name, r.StatusCode, err)
		}
		if !near(m.At(30, 64), 255, 0, 0) || !near(m.At(225, 64), 0, 0, 255) {
			t.Errorf("%s: colours %v %v", name, m.At(30, 64), m.At(225, 64))
		}
	}
	r, b := getThumb(t, x.url+"/thumb/gray.png", nil)
	if m, _, err := image.Decode(bytes.NewReader(b)); r.StatusCode != 200 || err != nil || !near(m.At(100, 60), 200, 200, 200) {
		t.Fatalf("gray: %d %v", r.StatusCode, err)
	}
}
