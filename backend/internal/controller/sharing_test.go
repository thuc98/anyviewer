package controller

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestShareScreenRoundTrip(t *testing.T) {
	src := image.NewRGBA(image.Rect(10, 20, 45, 53))
	for y := src.Rect.Min.Y; y < src.Rect.Max.Y; y++ {
		for x := src.Rect.Min.X; x < src.Rect.Max.X; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 5), G: uint8(y * 3), B: uint8(x + y), A: 255})
		}
	}
	payload, err := encodeShareScreen(src)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := decodeByteArrayFields(payload)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := newZstdDictDecoder(defaultDictionary)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.dec.Close()
	screen, err := newRGB555Screen(1, 1, dec, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas.Rect != image.Rect(0, 0, 35, 33) {
		t.Fatalf("unexpected screen size: %v", screen.canvas.Rect)
	}
	for y := 0; y < 33; y++ {
		for x := 0; x < 35; x++ {
			original := src.RGBAAt(x+10, y+20)
			want := color.RGBA{R: uint8(int(original.R>>3) * 255 / 31), G: uint8(int(original.G>>3) * 255 / 31), B: uint8(int(original.B>>3) * 255 / 31), A: 255}
			if got := screen.canvas.RGBAAt(x, y); got != want {
				t.Fatalf("pixel (%d,%d): got %v, want %v", x, y, got, want)
			}
		}
	}
	for _, src := range []image.Image{nil, image.NewRGBA(image.Rect(0, 0, 0, 1)), image.NewUniform(color.Black)} {
		if _, err := encodeShareScreen(src); err == nil {
			t.Fatal("accepted invalid frame geometry")
		}
	}
}

type recordingShareTarget struct {
	inputs    []shareInput
	clipboard string
	released  bool
}

func (s *recordingShareTarget) Input(event shareInput) error {
	s.inputs = append(s.inputs, event)
	return nil
}
func (s *recordingShareTarget) SetClipboard(text string) error { s.clipboard = text; return nil }
func (s *recordingShareTarget) ReleaseInputs() error           { s.released = true; return nil }

func TestShareInputCommandsAndPermissions(t *testing.T) {
	target := &recordingShareTarget{}
	controls := shareControls{capabilities: shareCapabilities{Keyboard: true, Mouse: true, Clipboard: true}, target: target}
	cases := []struct {
		payload []byte
		event   shareInput
	}{
		{mustShareCommand(mouseMove(-10, 20)), shareInput{Type: "move", X: -10, Y: 20}},
		{mustShareCommand(mouseButton("left", true)), shareInput{Type: "button", Button: "left", Down: true}},
		{mustShareCommand(mouseButton("right", false)), shareInput{Type: "button", Button: "right"}},
		{mustShareCommand(mouseButton("middle", true)), shareInput{Type: "button", Button: "middle", Down: true}},
		{mustShareCommand(mouseWheel(-120)), shareInput{Type: "wheel", Delta: -120}},
		{mustShareCommand(keyboard(65, true, false)), shareInput{Type: "key", VK: 65, Down: true}},
		{mustShareCommand(keyboard(18, false, true)), shareInput{Type: "key", VK: 18, System: true}},
	}
	for i, tc := range cases {
		if err := controls.handleInput(tc.payload); err != nil {
			t.Fatal(err)
		}
		if target.inputs[i] != tc.event {
			t.Fatalf("input: got %+v, want %+v", target.inputs[i], tc.event)
		}
	}
	for _, payload := range []string{"8|||256|||256", "8|||512|||0|||32768|||0", "8|||513|||1", "8|||522|||1", "8|||999", "8|||", strings.Repeat("x", 129)} {
		if err := controls.handleInput([]byte(payload)); err == nil {
			t.Fatalf("accepted malformed command %q", payload)
		}
	}
	if err := controls.close(); err != nil || !target.released {
		t.Fatal("native inputs were not released on close")
	}
	controls.capabilities = shareCapabilities{Mouse: true}
	if err := controls.handleInput(mustShareCommand(keyboard(65, true, false))); err == nil {
		t.Fatal("mouse capability allowed keyboard input")
	}
	controls.capabilities = browserShareCapabilities
	before := len(target.inputs)
	for _, tc := range cases {
		if err := controls.handleInput(tc.payload); err == nil {
			t.Fatal("browser source accepted remote control")
		}
	}
	if err := controls.handleClipboard(5, [][]byte{{5}, utf16le("private")}); err == nil || target.clipboard != "" || len(target.inputs) != before {
		t.Fatal("browser source accessed a control adapter")
	}
}

func mustShareCommand(payload []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return payload
}

func TestShareClipboardUsesExistingWireFormats(t *testing.T) {
	target := &recordingShareTarget{}
	controls := shareControls{capabilities: shareCapabilities{Clipboard: true}, target: target}
	if err := controls.handleClipboard(5, [][]byte{{5}, utf16le("Xin chào 👋")}); err != nil || target.clipboard != "Xin chào 👋" {
		t.Fatal("plain clipboard failed", err)
	}
	text := strings.Repeat("Xin chào 👋|||", 100)
	payload, _, err := clipboardCommand(text)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.SplitN(payload, []byte("|||"), 3)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(utf16le(text))))
	if err := controls.handleClipboard(33, [][]byte{{33}, size, parts[2]}); err != nil || target.clipboard != text {
		t.Fatal("compressed clipboard failed", err)
	}
	if err := controls.handleClipboard(5, [][]byte{{5}, {0xff}}); err == nil || target.clipboard != text {
		t.Fatal("invalid clipboard changed target")
	}
}
