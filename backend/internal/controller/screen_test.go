package controller

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func screenRectangle(x, y, w, h int) []byte {
	record := make([]byte, 8)
	for i, v := range []int{x, y, w, h} {
		binary.LittleEndian.PutUint16(record[i*2:], uint16(v))
	}
	return record
}

func TestScreenExpandsForJPEGAndCache(t *testing.T) {
	screen, err := newRGB555Screen(1024, 768, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	screen.canvas.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	atlas := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			atlas.SetRGBA(x, y, color.RGBA{G: 255, A: 255})
		}
	}
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, atlas, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[7] = jpegData.Bytes()
	fields[8] = screenRectangle(1888, 1048, 32, 32)
	fields[10] = append(screenRectangle(1888, 1048, 32, 32), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(fields[10][8:], 123)
	if _, err := screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.width != 1920 || screen.height != 1080 {
		t.Fatalf("canvas = %dx%d", screen.width, screen.height)
	}
	if screen.canvas.RGBAAt(0, 0).R != 255 || screen.canvas.RGBAAt(1919, 1079).G < 240 {
		t.Fatal("old or new pixels lost")
	}
	fields[7], fields[8], fields[10] = nil, nil, nil
	fields[9] = make([]byte, 8)
	binary.LittleEndian.PutUint16(fields[9], 2000)
	binary.LittleEndian.PutUint16(fields[9][2:], 1200)
	binary.LittleEndian.PutUint32(fields[9][4:], 123)
	if _, err := screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.width != 2032 || screen.height != 1232 || screen.canvas.RGBAAt(2031, 1231).G < 240 {
		t.Fatal("cache reference did not expand canvas")
	}
}

func TestScreenFailedExpansionIsAtomic(t *testing.T) {
	screen, err := newRGB555Screen(10, 10, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	before := screen.canvas
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[1] = screenRectangle(100, 100, 1, 1)
	fields[2] = []byte("invalid codec")
	if _, err := screen.apply(fields); err == nil {
		t.Fatal("accepted invalid codec")
	}
	if screen.canvas != before || screen.width != 10 || screen.height != 10 {
		t.Fatal("failed update committed canvas size")
	}
	for _, rect := range [][]byte{screenRectangle(-1, 0, 1, 1), screenRectangle(8192, 0, 1, 1), screenRectangle(0, 0, 8192, 8192)} {
		fields[1], fields[2] = nil, nil
		fields[10] = append(rect, 0, 0, 0, 0)
		if _, err := screen.apply(fields); err == nil {
			t.Fatal("accepted invalid or oversized rectangle")
		}
		if screen.canvas != before || screen.width != 10 || screen.height != 10 || len(screen.cache.items) != 0 {
			t.Fatal("invalid update modified screen")
		}
	}
}

func TestScreenStagesPatchesBeforeCommitAndTracksDirty(t *testing.T) {
	screen, err := newRGB555Screen(64, 64, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	tile := newCanvas(2, 2)
	tile.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	screen.cache.add(123, tile)
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[9] = make([]byte, 8)
	binary.LittleEndian.PutUint16(fields[9], 10)
	binary.LittleEndian.PutUint16(fields[9][2:], 20)
	binary.LittleEndian.PutUint32(fields[9][4:], 123)
	fields[7] = []byte("invalid JPEG")
	before := screen.canvas
	if _, err = screen.apply(fields); err == nil {
		t.Fatal("invalid trailing operation accepted")
	}
	if screen.canvas != before || screen.canvas.RGBAAt(10, 20).R != 0 {
		t.Fatal("staged cache patch committed before validation")
	}
	fields[7] = nil
	if _, err = screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas != before {
		t.Fatal("small update cloned full canvas")
	}
	if screen.dirty != image.Rect(10, 20, 12, 22) || screen.canvas.RGBAAt(10, 20).R != 255 {
		t.Fatal("dirty rectangle or in-place update incorrect")
	}
}

func TestScreenScrollCopiesOnlySourceRegion(t *testing.T) {
	screen, err := newRGB555Screen(64, 64, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	screen.canvas.SetRGBA(10, 20, color.RGBA{G: 255, A: 255})
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[6] = make([]byte, 10)
	binary.LittleEndian.PutUint16(fields[6], 5)
	copy(fields[6][2:], screenRectangle(10, 20, 2, 2))
	fields[6] = append(fields[6], screenRectangle(10, 15, 2, 2)...)
	before := screen.canvas
	if _, err = screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas != before || screen.dirty != image.Rect(10, 15, 12, 17) || screen.canvas.RGBAAt(10, 15).G != 255 {
		t.Fatal("scroll patch wrong or canvas copied")
	}
}
