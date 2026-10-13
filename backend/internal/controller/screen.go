// Decode observed RGB555 Zstandard rectangle updates, JPEG atlas tiles, tile
// cache references/additions and scroll-copy. Go port of screen_decode.py.
//
// Field mapping follows VRecordReader.ManipulatePacket (13a2) and drawer
// constructor c8b; worker c65 consumes references before pixel updates and saves
// cache additions afterwards. BitBlt remains an explicit unsupported operation.
package controller

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/klauspost/compress/zstd"
)

const maxDecodeOutput = 64 * 1024 * 1024

// zstdDictDecoder decompresses frames with a fixed dictionary.
type zstdDictDecoder struct {
	dec *zstd.Decoder
}

func newZstdDictDecoder(dictionary []byte) (*zstdDictDecoder, error) {
	dec, err := zstd.NewReader(nil,
		zstd.WithDecoderDicts(dictionary),
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(maxDecodeOutput))
	if err != nil {
		return nil, err
	}
	return &zstdDictDecoder{dec: dec}, nil
}

func (z *zstdDictDecoder) decompress(payload []byte, expectedSize int) ([]byte, error) {
	if expectedSize <= 0 || expectedSize > maxDecodeOutput {
		return nil, errors.New("decoded image exceeds allocation policy")
	}
	out, err := z.dec.DecodeAll(payload, make([]byte, 0, expectedSize))
	if err != nil {
		return nil, errors.New("Zstandard decoding failed or image size differs")
	}
	if len(out) != expectedSize {
		return nil, errors.New("Zstandard decoding failed or image size differs")
	}
	return out, nil
}

// ---- RGBA image helpers --------------------------------------------------

func newCanvas(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Opaque black background, matching Image.new('RGB', size) rendered to PNG.
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func cloneCanvas(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}

// pasteImage copies src onto dst at (dx,dy), clipping to dst bounds.
func pasteImage(dst, src *image.RGBA, dx, dy int) {
	sw := src.Rect.Dx()
	sh := src.Rect.Dy()
	dw := dst.Rect.Dx()
	dh := dst.Rect.Dy()
	for sy := 0; sy < sh; sy++ {
		ty := dy + sy
		if ty < 0 || ty >= dh {
			continue
		}
		for sx := 0; sx < sw; sx++ {
			tx := dx + sx
			if tx < 0 || tx >= dw {
				continue
			}
			si := src.PixOffset(src.Rect.Min.X+sx, src.Rect.Min.Y+sy)
			di := dst.PixOffset(tx, ty)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
}

// cropImage returns an independent copy of the (x,y,w,h) region of src.
func cropImage(src *image.RGBA, x, y, w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		si := src.PixOffset(x, y+row)
		di := out.PixOffset(0, row)
		copy(out.Pix[di:di+w*4], src.Pix[si:si+w*4])
	}
	return out
}

// ---- LRU tile cache ------------------------------------------------------

type tileCache struct {
	items map[int32]*image.RGBA
	order []int32 // front = least recently used
	limit int
}

func newTileCache(limit int) *tileCache {
	return &tileCache{items: map[int32]*image.RGBA{}, order: nil, limit: limit}
}

func (c *tileCache) clone() *tileCache {
	n := &tileCache{items: make(map[int32]*image.RGBA, len(c.items)), limit: c.limit}
	for k, v := range c.items {
		n.items[k] = v
	}
	n.order = append([]int32(nil), c.order...)
	return n
}

func (c *tileCache) get(key int32) (*image.RGBA, bool) {
	v, ok := c.items[key]
	return v, ok
}

func (c *tileCache) removeOrder(key int32) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// touch moves an existing key to the most-recently-used position.
func (c *tileCache) touch(key int32) {
	c.removeOrder(key)
	c.order = append(c.order, key)
}

// add inserts a new key (only when absent), appended as most recently used.
func (c *tileCache) add(key int32, img *image.RGBA) {
	if _, ok := c.items[key]; ok {
		return
	}
	c.items[key] = img
	c.order = append(c.order, key)
}

func (c *tileCache) trim() {
	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
}

// ---- RGB555Screen --------------------------------------------------------

type rgb555Screen struct {
	canvas    *image.RGBA
	decoder   *zstdDictDecoder
	cache     *tileCache
	blockSize int
	width     int
	height    int
}

func newRGB555Screen(w, h int, decoder *zstdDictDecoder, blockSize int) (*rgb555Screen, error) {
	if blockSize <= 0 {
		return nil, errors.New("JPEG block size must be positive")
	}
	return &rgb555Screen{
		canvas:    newCanvas(w, h),
		decoder:   decoder,
		cache:     newTileCache(1000),
		blockSize: blockSize,
		width:     w,
		height:    h,
	}, nil
}

func (s *rgb555Screen) bounds(x, y, w, h int) error {
	if w <= 0 || h <= 0 || x < 0 || y < 0 || x+w > s.width || y+h > s.height {
		return fmt.Errorf("cache rectangle (%d,%d %dx%d) outside canvas %dx%d", x, y, w, h, s.width, s.height)
	}
	return nil
}

func i16(b []byte) int   { return int(int16(binary.LittleEndian.Uint16(b))) }
func i32(b []byte) int32 { return int32(binary.LittleEndian.Uint32(b)) }
func u16(b []byte) int   { return int(binary.LittleEndian.Uint16(b)) }

// updateSize derives the minimum canvas needed by validated rectangle records.
// The wire format does not provide a negotiated size in this recovered path.
func (s *rgb555Screen) updateSize(fields [][]byte) (int, int, error) {
	width, height := s.width, s.height
	include := func(x, y, w, h int) error {
		if x < 0 || y < 0 || w <= 0 || h <= 0 {
			return fmt.Errorf("invalid screen rectangle (%d,%d %dx%d)", x, y, w, h)
		}
		width = max(width, x+w)
		height = max(height, y+h)
		if width > 8192 || height > 8192 || width*height > 16*1024*1024 {
			return fmt.Errorf("screen extent %dx%d exceeds allocation policy", width, height)
		}
		return nil
	}
	for _, index := range []int{1, 8, 10} {
		stride := 8
		if index == 10 {
			stride = 12
		}
		if len(fields[index])%stride != 0 {
			return 0, 0, errors.New("invalid screen rectangle record length")
		}
		for off := 0; off < len(fields[index]); off += stride {
			record := fields[index][off:]
			x, y, w, h := i16(record), i16(record[2:]), i16(record[4:]), i16(record[6:])
			if index == 1 {
				x, y, w, h = u16(record), u16(record[2:]), u16(record[4:]), u16(record[6:])
			}
			if err := include(x, y, w, h); err != nil {
				return 0, 0, err
			}
		}
	}
	if len(fields[6]) != 0 {
		if len(fields[6]) < 10 || (len(fields[6])-10)%8 != 0 {
			return 0, 0, errors.New("invalid scroll-copy record length")
		}
		record := fields[6]
		if err := include(i16(record[2:]), i16(record[4:]), i16(record[6:]), i16(record[8:])); err != nil {
			return 0, 0, err
		}
		for off := 10; off < len(record); off += 8 {
			r := record[off:]
			if err := include(i16(r), i16(r[2:]), i16(r[4:]), i16(r[6:])); err != nil {
				return 0, 0, err
			}
		}
	}
	for off := 0; off < len(fields[9]); off += 8 {
		record := fields[9][off:]
		tile, ok := s.cache.get(i32(record[4:]))
		if !ok {
			return 0, 0, errors.New("screen cache reference is missing")
		}
		if err := include(i16(record), i16(record[2:]), tile.Rect.Dx(), tile.Rect.Dy()); err != nil {
			return 0, 0, err
		}
	}
	return width, height, nil
}

// apply decodes one eleven-field kind1 update onto a working copy, committing the
// new canvas and cache only when every rectangle validates.
func (s *rgb555Screen) apply(fields [][]byte) (int, error) {
	if len(fields) != 11 || len(fields[0]) == 0 || fields[0][0] != 1 {
		return 0, errors.New("expected eleven-field kind1 update")
	}
	if len(fields[5]) != 0 {
		return 0, errors.New("BitBlt screen operations are not supported yet")
	}
	if len(fields[9])%8 != 0 || len(fields[10])%12 != 0 {
		return 0, errors.New("invalid screen cache record length")
	}
	width, height, err := s.updateSize(fields)
	if err != nil {
		return 0, err
	}
	original := s
	working := *s
	s = &working
	s.width, s.height = width, height
	var canvas *image.RGBA
	if width == s.canvas.Rect.Dx() && height == s.canvas.Rect.Dy() {
		canvas = cloneCanvas(s.canvas)
	} else {
		canvas = newCanvas(width, height)
		pasteImage(canvas, s.canvas, 0, 0)
	}
	cache := s.cache.clone()
	count := 0

	// Scroll-copy (fields[6]).
	if len(fields[6]) != 0 {
		if len(fields[6]) < 10 || (len(fields[6])-10)%8 != 0 {
			return 0, errors.New("invalid scroll-copy record length")
		}
		delta := i16(fields[6][0:2])
		x := i16(fields[6][2:4])
		y := i16(fields[6][4:6])
		w := i16(fields[6][6:8])
		h := i16(fields[6][8:10])
		if err := s.bounds(x, y, w, h); err != nil {
			return 0, err
		}
		// c65 copies old screen into an intermediate bitmap at (x,y-delta), then
		// restores listed subrectangles.
		shifted := newCanvas(s.width, s.height)
		pasteImage(shifted, cropImage(canvas, x, y, w, h), x, y-delta)
		rest := fields[6][10:]
		for off := 0; off < len(rest); off += 8 {
			rx := i16(rest[off : off+2])
			ry := i16(rest[off+2 : off+4])
			rw := i16(rest[off+4 : off+6])
			rh := i16(rest[off+6 : off+8])
			if err := s.bounds(rx, ry, rw, rh); err != nil {
				return 0, err
			}
			if rx < x || rx+rw > x+w || ry < y-delta || ry+rh > y-delta+h {
				return 0, errors.New("scroll-copy references uncopied intermediate pixels")
			}
			pasteImage(canvas, cropImage(shifted, rx, ry, rw, rh), rx, ry)
			count++
		}
	}

	// Cache references (fields[9]): records of (int16 x, int16 y, int32 key).
	for off := 0; off < len(fields[9]); off += 8 {
		x := i16(fields[9][off : off+2])
		y := i16(fields[9][off+2 : off+4])
		key := i32(fields[9][off+4 : off+8])
		tile, ok := cache.get(key)
		if !ok {
			return 0, errors.New("screen cache reference is missing")
		}
		if err := s.bounds(x, y, tile.Rect.Dx(), tile.Rect.Dy()); err != nil {
			return 0, err
		}
		pasteImage(canvas, tile, x, y)
		cache.touch(key)
		count++
	}

	// JPEG atlas (fields[7] image, fields[8] rectangles).
	if len(fields[7]) != 0 || len(fields[8]) != 0 {
		if !bytes.HasPrefix(fields[7], []byte{0xff, 0xd8}) || len(fields[8]) == 0 || len(fields[8])%8 != 0 {
			return 0, errors.New("invalid JPEG atlas fields")
		}
		src, err := jpeg.Decode(bytes.NewReader(fields[7]))
		if err != nil {
			return 0, errors.New("invalid JPEG atlas fields")
		}
		aw := src.Bounds().Dx()
		ah := src.Bounds().Dy()
		if aw*ah > 32*1024*1024 {
			return 0, errors.New("JPEG atlas outside allocation policy")
		}
		// Native TJFLAG_BOTTOMUP then ordinal60 reads increasing rows: flip
		// vertically into an RGBA atlas.
		atlas := image.NewRGBA(image.Rect(0, 0, aw, ah))
		sb := src.Bounds()
		for row := 0; row < ah; row++ {
			srcY := sb.Max.Y - 1 - row
			for col := 0; col < aw; col++ {
				r, g, b, _ := src.At(sb.Min.X+col, srcY).RGBA()
				di := atlas.PixOffset(col, row)
				atlas.Pix[di+0] = uint8(r >> 8)
				atlas.Pix[di+1] = uint8(g >> 8)
				atlas.Pix[di+2] = uint8(b >> 8)
				atlas.Pix[di+3] = 255
			}
		}
		var rects [][4]int
		for off := 0; off < len(fields[8]); off += 8 {
			x := i16(fields[8][off : off+2])
			y := i16(fields[8][off+2 : off+4])
			w := i16(fields[8][off+4 : off+6])
			h := i16(fields[8][off+6 : off+8])
			if err := s.bounds(x, y, w, h); err != nil {
				return 0, err
			}
			rects = append(rects, [4]int{x, y, w, h})
		}
		tiles, err := atlasLayout(rects, s.blockSize, ah)
		if err != nil {
			return 0, err
		}
		for _, t := range tiles {
			if t.sx+t.w > aw || t.sy+t.h > ah {
				return 0, errors.New("JPEG tile outside atlas")
			}
			pasteImage(canvas, cropImage(atlas, t.sx, t.sy, t.w, t.h), t.x, t.y)
			count++
		}
	}

	// RGB555 pixel update (fields[1] rectangles, fields[2] zstd stream).
	if len(fields[1]) != 0 && len(fields[2]) != 0 {
		n, err := s.applyRGB555(canvas, fields[1], fields[2])
		if err != nil {
			return 0, err
		}
		count += n
	}

	// Cache additions (fields[10]): records of (int16 x,y,w,h, int32 key).
	for off := 0; off < len(fields[10]); off += 12 {
		x := i16(fields[10][off : off+2])
		y := i16(fields[10][off+2 : off+4])
		w := i16(fields[10][off+4 : off+6])
		h := i16(fields[10][off+6 : off+8])
		key := i32(fields[10][off+8 : off+12])
		if err := s.bounds(x, y, w, h); err != nil {
			return 0, err
		}
		if _, ok := cache.get(key); !ok {
			cache.add(key, cropImage(canvas, x, y, w, h))
		}
	}

	cache.trim()
	original.canvas = canvas
	original.cache = cache
	original.width, original.height = width, height
	return count, nil
}

// applyRGB555 validates and applies the Zstandard RGB555 rectangle stream.
func (s *rgb555Screen) applyRGB555(canvas *image.RGBA, rectField, pixelField []byte) (int, error) {
	if len(rectField) == 0 || len(rectField)%8 != 0 {
		return 0, errors.New("expected UInt16 x/y/w/h rectangle records")
	}
	if !bytes.HasPrefix(pixelField, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return 0, errors.New("screen codec is outside supported Zstandard subset")
	}
	type rect struct{ x, y, w, h int }
	var rects []rect
	total := 0
	for off := 0; off < len(rectField); off += 8 {
		x := u16(rectField[off : off+2])
		y := u16(rectField[off+2 : off+4])
		w := u16(rectField[off+4 : off+6])
		h := u16(rectField[off+6 : off+8])
		if w == 0 || h == 0 || x+w > s.width || y+h > s.height {
			return 0, errors.New("screen rectangle outside canvas")
		}
		rects = append(rects, rect{x, y, w, h})
		total += w * h * 2
	}
	raw, err := s.decoder.decompress(pixelField, total)
	if err != nil {
		return 0, err
	}
	offset := 0
	for _, r := range rects {
		tile := image.NewRGBA(image.Rect(0, 0, r.w, r.h))
		for py := 0; py < r.h; py++ {
			for px := 0; px < r.w; px++ {
				word := binary.LittleEndian.Uint16(raw[offset : offset+2])
				offset += 2
				di := tile.PixOffset(px, py)
				tile.Pix[di+0] = uint8(((word >> 10) & 31) * 255 / 31) // R (bits 10-14)
				tile.Pix[di+1] = uint8(((word >> 5) & 31) * 255 / 31)  // G (bits 5-9)
				tile.Pix[di+2] = uint8((word & 31) * 255 / 31)         // B (bits 0-4)
				tile.Pix[di+3] = 255
			}
		}
		pasteImage(canvas, tile, r.x, r.y)
	}
	return len(rects), nil
}
