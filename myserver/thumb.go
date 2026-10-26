package myserver

// Thumbnails, so a folder of photos lists without fetching every original:
//
//	GET /thumb/<path>?w=256      a small JPEG of the image (PNG when it has
//	                             transparency), w its longest side
//
// w rounds up to one of thumbSides. JPEG, PNG, GIF (its first frame) and WebP
// are read; a JPEG's EXIF orientation is applied, as browsers do. An image
// already that small is sent as it is. Thumbnails are made once and kept in
// .myweb/thumbs, named by the file's path, size and time, so a changed file
// gets a new one; those not asked for in thumbKeep are removed. Reading one
// takes the same right as reading the image.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

var thumbSides = []int{64, 128, 256, 512, 1024}

const (
	thumbMaxBytes  = 200 << 20 // larger files aren't read
	thumbMaxPixels = 50 << 20  // nor larger images decoded (each costs ~4 bytes a pixel)
	thumbAsIsBytes = 256 << 10 // an image within the size, and no bigger than this, is sent as it is
	thumbKeep      = 30 * 24 * time.Hour
)

var errNotImage = errors.New("not an image this server makes thumbnails of")

type thumbMaker struct {
	once   sync.Once
	slots  chan struct{} // how many are made at once: decoding takes memory
	mu     sync.Mutex
	making map[string]chan struct{} // one maker per thumbnail; others wait for it
	pruned time.Time
}

func (t *thumbMaker) init() {
	t.once.Do(func() {
		t.slots = make(chan struct{}, min(runtime.NumCPU(), 4))
		t.making = map[string]chan struct{}{}
	})
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "get only")
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if IsReserved(p) || !s.can(r, ActRead, p) {
		http.NotFound(w, r)
		return
	}
	fp := s.ResolvePath(p)
	fi, err := os.Stat(fp)
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if fi.Size() > thumbMaxBytes {
		writeError(w, http.StatusUnsupportedMediaType, "too large for a thumbnail")
		return
	}
	side := thumbSide(r.URL.Query().Get("w"))
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%d", p, fi.Size(), fi.ModTime().UnixNano(), side)))
	key := hex.EncodeToString(h[:12])
	etag := `"t` + key + `"`
	w.Header().Set("ETag", etag)
	if r.URL.Query().Get("v") != "" {
		// the page names the file's version in the address: a change asks anew
		w.Header().Set("Cache-Control", "private, max-age=604800")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, ctype, err := s.thumb(fp, key, side)
	switch {
	case errors.Is(err, errNotImage):
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	case err != nil:
		log.Printf("thumbnail of %s: %v", p, err)
		writeError(w, http.StatusUnprocessableEntity, "cannot make a thumbnail of "+p)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet {
		w.Write(data)
	}
}

// thumbSide is the smallest of thumbSides at least w (256 when w isn't given).
func thumbSide(q string) int {
	w, err := strconv.Atoi(q)
	if err != nil || w <= 0 {
		return 256
	}
	for _, s := range thumbSides {
		if w <= s {
			return s
		}
	}
	return thumbSides[len(thumbSides)-1]
}

// thumb returns the thumbnail of the file fp, from the cache or made now.
func (s *Server) thumb(fp, key string, side int) ([]byte, string, error) {
	t := &s.thumbs
	t.init()
	dir := s.metaPath("thumbs", key[:2])
	cached := func() ([]byte, string, bool) {
		for _, c := range []struct{ ext, ctype string }{{".jpg", "image/jpeg"}, {".png", "image/png"}} {
			f := filepath.Join(dir, key+c.ext)
			if data, err := os.ReadFile(f); err == nil {
				if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
					now := time.Now()
					os.Chtimes(f, now, now) // in use: not pruned
				}
				return data, c.ctype, true
			}
		}
		return nil, "", false
	}
	for {
		if data, ctype, ok := cached(); ok {
			return data, ctype, nil
		}
		t.mu.Lock()
		wait, busy := t.making[key]
		if !busy {
			t.making[key] = make(chan struct{})
		}
		t.mu.Unlock()
		if !busy {
			break
		}
		<-wait // made by another request: look again
	}
	defer func() {
		t.mu.Lock()
		close(t.making[key])
		delete(t.making, key)
		t.mu.Unlock()
	}()
	t.slots <- struct{}{}
	data, ext, asIs, err := makeThumb(fp, side)
	<-t.slots
	if err != nil {
		return nil, "", err
	}
	ctype := map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp"}[ext]
	if asIs {
		return data, ctype, nil // nothing to keep
	}
	if err := writeFileAtomic(filepath.Join(dir, key+ext), data); err != nil {
		log.Printf("keeping a thumbnail: %v", err) // served anyway
	}
	s.pruneThumbsSometimes()
	return data, ctype, nil
}

// makeThumb reads the image at fp and returns its thumbnail and the
// extension of its type; asIs: the file itself, already small enough.
func makeThumb(fp string, side int) (data []byte, ext string, asIs bool, err error) {
	f, err := os.Open(fp)
	if err != nil {
		return nil, "", false, err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return nil, "", false, errNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return nil, "", false, fmt.Errorf("%dx%d pixels: too many", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", false, err
	}
	orient := 1
	if format == "jpeg" {
		orient = jpegOrientation(f)
		f.Seek(0, io.SeekStart)
	}
	if cfg.Width <= side && cfg.Height <= side && orient == 1 {
		if fi, err := f.Stat(); err == nil && fi.Size() <= thumbAsIsBytes {
			data, err := io.ReadAll(f)
			return data, map[string]string{"jpeg": ".jpg", "png": ".png", "gif": ".gif", "webp": ".webp"}[format], true, err
		}
	}
	src, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, "", false, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > side || h > side {
		if w >= h {
			w, h = side, max(1, h*side/w)
		} else {
			w, h = max(1, w*side/h), side
		}
	}
	// a big original is first averaged down to about twice the size (quick),
	// then scaled finely: CatmullRom over every pixel of a photo takes long
	from := src
	if k := min(b.Dx()/w, b.Dy()/h) / 2; k >= 2 {
		from = shrink(src, k)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), from, from.Bounds(), xdraw.Src, nil)
	out := orientImage(dst, orient)
	var buf bytes.Buffer
	if opaque(src) {
		err = jpeg.Encode(&buf, out, &jpeg.Options{Quality: 82})
		ext = ".jpg"
	} else {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, out)
		ext = ".png"
	}
	return buf.Bytes(), ext, false, err
}

// shrink returns src made k times smaller by averaging each k×k block.
func shrink(src image.Image, k int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/k, b.Dy()/k
	d := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint32(k * k)
	switch m := src.(type) {
	case *image.YCbCr: // JPEG: average Y, Cb and Cr, then convert once
		for oy := 0; oy < h; oy++ {
			for ox := 0; ox < w; ox++ {
				var sy, scb, scr uint32
				x0 := b.Min.X + ox*k
				for y := b.Min.Y + oy*k; y < b.Min.Y+oy*k+k; y++ {
					yi := m.YOffset(x0, y)
					for x := x0; x < x0+k; x++ {
						ci := m.COffset(x, y)
						sy += uint32(m.Y[yi])
						scb += uint32(m.Cb[ci])
						scr += uint32(m.Cr[ci])
						yi++
					}
				}
				r, g, bl := color.YCbCrToRGB(uint8(sy/n), uint8(scb/n), uint8(scr/n))
				i := d.PixOffset(ox, oy)
				d.Pix[i], d.Pix[i+1], d.Pix[i+2], d.Pix[i+3] = r, g, bl, 0xff
			}
		}
		return d
	case *image.NRGBA: // PNG with transparency: weigh colours by alpha
		for oy := 0; oy < h; oy++ {
			for ox := 0; ox < w; ox++ {
				var sr, sg, sb, sa uint32
				for y := oy * k; y < oy*k+k; y++ {
					i := m.PixOffset(b.Min.X+ox*k, b.Min.Y+y)
					for x := 0; x < k; x++ {
						a := uint32(m.Pix[i+3])
						sr += uint32(m.Pix[i]) * a
						sg += uint32(m.Pix[i+1]) * a
						sb += uint32(m.Pix[i+2]) * a
						sa += a
						i += 4
					}
				}
				i := d.PixOffset(ox, oy) // premultiplied, as RGBA is
				d.Pix[i], d.Pix[i+1], d.Pix[i+2], d.Pix[i+3] = uint8(sr/255/n), uint8(sg/255/n), uint8(sb/255/n), uint8(sa/n)
			}
		}
		return d
	}
	rgba, ok := src.(*image.RGBA)
	if !ok { // other kinds: as RGBA first
		rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	}
	rb := rgba.Bounds()
	for oy := 0; oy < h; oy++ {
		for ox := 0; ox < w; ox++ {
			var s [4]uint32
			for y := oy * k; y < oy*k+k; y++ {
				i := rgba.PixOffset(rb.Min.X+ox*k, rb.Min.Y+y)
				for x := 0; x < k; x++ {
					s[0] += uint32(rgba.Pix[i])
					s[1] += uint32(rgba.Pix[i+1])
					s[2] += uint32(rgba.Pix[i+2])
					s[3] += uint32(rgba.Pix[i+3])
					i += 4
				}
			}
			i := d.PixOffset(ox, oy)
			d.Pix[i], d.Pix[i+1], d.Pix[i+2], d.Pix[i+3] = uint8(s[0]/n), uint8(s[1]/n), uint8(s[2]/n), uint8(s[3]/n)
		}
	}
	return d
}

// opaque: has the image no transparent pixel? (formats without alpha never)
func opaque(img image.Image) bool {
	switch m := img.(type) {
	case *image.YCbCr, *image.Gray, *image.Gray16, *image.CMYK:
		return true
	case *image.Paletted:
		for _, c := range m.Palette {
			if _, _, _, a := c.RGBA(); a != 0xffff {
				return false
			}
		}
		return true
	case interface{ Opaque() bool }:
		return m.Opaque()
	}
	return false
}

// jpegOrientation reads the EXIF orientation (1–8) of the JPEG r; 1 when it
// has none.
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(io.LimitReader(r, 256<<10))
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:2]); err != nil || hdr[0] != 0xff || hdr[1] != 0xd8 {
		return 1
	}
	for {
		if _, err := io.ReadFull(br, hdr[:4]); err != nil || hdr[0] != 0xff {
			return 1
		}
		marker, n := hdr[1], int(binary.BigEndian.Uint16(hdr[2:]))-2
		if marker == 0xda || n < 0 { // the image data: no EXIF before it
			return 1
		}
		seg := make([]byte, n)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if marker == 0xe1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
	}
}

func exifOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[ifd:]))
	for i := 0; i < n; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 { // Orientation, a SHORT
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			break
		}
	}
	return 1
}

// orientImage turns m upright for EXIF orientation o.
func orientImage(m *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return m
	}
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	d := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			d.SetRGBA(dx, dy, m.RGBAAt(x, y))
		}
	}
	return d
}

// pruneThumbsSometimes removes, at most once a day, thumbnails not asked for
// in thumbKeep.
func (s *Server) pruneThumbsSometimes() {
	t := &s.thumbs
	t.mu.Lock()
	due := time.Since(t.pruned) > 24*time.Hour
	if due {
		t.pruned = time.Now()
	}
	t.mu.Unlock()
	if !due {
		return
	}
	go func() {
		cutoff := time.Now().Add(-thumbKeep)
		filepath.WalkDir(s.metaPath("thumbs"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if fi, err := d.Info(); err == nil && fi.ModTime().Before(cutoff) {
					os.Remove(p)
				}
			}
			return nil
		})
	}()
}
