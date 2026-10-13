package controller

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/klauspost/compress/zstd"
	"os"
	"reflect"
	"testing"
)

func TestEncryptRegistrationBytes(t *testing.T) {
	// Cross-checked against openssl des-ede3-cbc with the derived key/IV.
	got, err := encryptRegistrationBytes([]byte("3:hello-ultraviewer"))
	if err != nil {
		t.Fatal(err)
	}
	want := "9554aa781a7fd5881506321803c9d2e9c30e94a8536f79dd"
	if hex.EncodeToString(got) != want {
		t.Fatalf("ciphertext = %s, want %s", hex.EncodeToString(got), want)
	}
}

func TestByteArrayFieldsRoundTrip(t *testing.T) {
	fields := [][]byte{{1}, {}, []byte("hello"), {0xff, 0x00, 0x10}}
	enc := encodeByteArrayFields(fields)
	want := []byte{
		1, 0, 0, 0, 1,
		0, 0, 0, 0,
		5, 0, 0, 0, 'h', 'e', 'l', 'l', 'o',
		3, 0, 0, 0, 0xff, 0x00, 0x10,
	}
	if !bytes.Equal(enc, want) {
		t.Fatalf("encode = %v, want %v", enc, want)
	}
	dec, err := decodeByteArrayFields(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dec, fields) {
		t.Fatalf("decode = %v, want %v", dec, fields)
	}
	if _, err := decodeByteArrayFields([]byte{5, 0, 0, 0, 'a'}); err == nil {
		t.Fatal("expected truncated field error")
	}
}

func TestEncodeTransportFrame(t *testing.T) {
	got, _ := encodeTransportFrame([]byte("hi"))
	if !bytes.Equal(got, []byte{2, 0, 0, 0, 'h', 'i'}) {
		t.Fatalf("frame = %v", got)
	}
}

func TestEncodeRelayFrame(t *testing.T) {
	got, err := encodeRelayFrame([]byte("abc"), "1.2.3.4", 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	// body: [0, flag, addr(LE of 0x01020304), port(LE)] + "abc"
	want := []byte{11, 0, 0, 0, 0, 0, 0x04, 0x03, 0x02, 0x01, 0x07, 0x00, 'a', 'b', 'c'}
	if !bytes.Equal(got, want) {
		t.Fatalf("relay frame = %v, want %v", got, want)
	}
	if _, err := encodeRelayFrame(nil, "1.2.3", 7, 0); err == nil {
		t.Fatal("expected octet error")
	}
	if _, err := encodeRelayFrame(nil, "1.2.3.400", 7, 0); err == nil {
		t.Fatal("expected octet range error")
	}
}

func TestDecodeTransportBody(t *testing.T) {
	body := []byte{0, 1, 0x04, 0x03, 0x02, 0x01, 0x07, 0x00, 'x', 'y'}
	p, err := decodeTransportBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if p.kind != "relay" || !p.flag || p.port != 7 || !bytes.Equal(p.data, []byte("xy")) {
		t.Fatalf("relay packet = %+v", p)
	}
	d, _ := decodeTransportBody([]byte{1, 'z'})
	if d.kind != "direct" || !bytes.Equal(d.data, []byte("z")) {
		t.Fatalf("direct packet = %+v", d)
	}
	if u, _ := decodeTransportBody([]byte{9}); u != nil {
		t.Fatal("expected nil for unknown kind")
	}
}

func TestInputCommands(t *testing.T) {
	cases := []struct {
		got  func() ([]byte, error)
		want string
	}{
		{func() ([]byte, error) { return mouseMove(10, 20) }, "8|||512|||0|||10|||20"},
		{func() ([]byte, error) { return mouseButton("left", true) }, "8|||513"},
		{func() ([]byte, error) { return mouseButton("right", false) }, "8|||517"},
		{func() ([]byte, error) { return mouseButton("middle", true) }, "8|||519"},
		{func() ([]byte, error) { return mouseWheel(120) }, "8|||522|||7864320"},
		{func() ([]byte, error) { return keyboard(65, true, false) }, "8|||256|||65"},
		{func() ([]byte, error) { return keyboard(65, false, false) }, "8|||257|||65"},
		{func() ([]byte, error) { return keyboard(65, true, true) }, "8|||260|||65"},
	}
	for i, c := range cases {
		got, err := c.got()
		if err != nil {
			t.Fatalf("case %d error: %v", i, err)
		}
		if string(got) != c.want {
			t.Fatalf("case %d = %q, want %q", i, got, c.want)
		}
	}
	if _, err := mouseButton("scroll", true); err == nil {
		t.Fatal("expected bad button error")
	}
	if _, err := keyboard(999, true, false); err == nil {
		t.Fatal("expected vk range error")
	}
}

func TestExtractCommand3Identity(t *testing.T) {
	id, err := extractCommand3Identity([]byte("3:443:12345:200.0.0.1"), "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	if id.clientID != 12345 || id.publicSourcePort != 443 || id.publicIP != "200.0.0.1" {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := extractCommand3Identity([]byte("9:1:2:3"), "cp1252"); err == nil {
		t.Fatal("expected wrong-command error")
	}
}

func TestDecodeTargetRoute(t *testing.T) {
	payload := []byte("8:1:192.168.1.5:x:9.9.9.9:5000:5001:5002:COMPNAME:4000:y:1.1.1.1:0:123456:relay.host")
	r, err := decodeTargetRoute(payload, "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	if r.publicIP != "9.9.9.9" || r.publicPort != 5000 || r.tcpPort != 4000 ||
		r.tcpServerIP != "1.1.1.1" || r.tcpServerHostname != "relay.host" || r.targetSessionID != 123456 {
		t.Fatalf("route = %+v", r)
	}
	if _, err := decodeTargetRoute([]byte("8:2:too:short"), "cp1252"); err == nil {
		t.Fatal("expected status/length error")
	}
}

func TestPasswordDigests(t *testing.T) {
	got, err := connectionRequestPasswordValue("27296", 0, "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fecd7699a12b297afcebad789418db32" {
		t.Fatalf("type0 password value = %s", got)
	}
	if v, _ := connectionRequestPasswordValue("27296", 1, "cp1252"); v != "27296" {
		t.Fatalf("non-type0 should reuse string, got %s", v)
	}
	salt, err := loginNumericSaltDigest("27296", "12345678", "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	if salt != "4901a30c29845c0c0cd5047a2d91ef5a" {
		t.Fatalf("salt digest = %s", salt)
	}
	if _, err := loginNumericSaltDigest("27296", "1234", "cp1252"); err == nil {
		t.Fatal("expected salt length error")
	}
}

func TestBuildLoginWithPassword(t *testing.T) {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = ""
	}
	fields[9] = "12345"
	out, err := buildLoginWithPassword(fields, "27296", 0, "12345678", "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	// Decode UTF-16LE back and confirm the sensitive positions were filled.
	if len(out)%2 != 0 {
		t.Fatal("login command must be UTF-16LE")
	}
	runes := make([]rune, 0, len(out)/2)
	for i := 0; i < len(out); i += 2 {
		runes = append(runes, rune(uint16(out[i])|uint16(out[i+1])<<8))
	}
	text := string(runes)
	if !bytes.HasPrefix([]byte(text), []byte("login:")) {
		t.Fatalf("missing login prefix: %q", text)
	}
	parts := splitColon(text[len("login:"):])
	if len(parts) != 22 {
		t.Fatalf("expected 22 fields, got %d", len(parts))
	}
	if parts[0] != "12345678" {
		t.Fatalf("salt field = %q", parts[0])
	}
	if parts[1] != "4901a30c29845c0c0cd5047a2d91ef5a" {
		t.Fatalf("primary = %q", parts[1])
	}
	if parts[12] != "fecd7699a12b297afcebad789418db32" {
		t.Fatalf("secondary = %q", parts[12])
	}
}

func splitColon(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ':' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	out = append(out, cur)
	return out
}

func TestAtlasLayout(t *testing.T) {
	// One 40x40 rectangle with block size 32 and a 64-high atlas: four tiles.
	tiles, err := atlasLayout([][4]int{{0, 0, 40, 40}}, 32, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 4 {
		t.Fatalf("expected 4 tiles, got %d", len(tiles))
	}
	// First tile: top-left 32x32 at atlas (0,0).
	if tiles[0] != (atlasTile{x: 0, y: 0, w: 32, h: 32, sx: 0, sy: 0}) {
		t.Fatalf("tile0 = %+v", tiles[0])
	}
	// Second tile: (0,32) 32x8, stacked below in the same column at atlas y=32.
	if tiles[1] != (atlasTile{x: 0, y: 32, w: 32, h: 8, sx: 0, sy: 32}) {
		t.Fatalf("tile1 = %+v", tiles[1])
	}
}

func TestRGB555Mapping(t *testing.T) {
	dict, err := os.ReadFile("8bit.dat")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := newZstdDictDecoder(dict)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.dec.Close()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	// Pillow's BGR;15 output for all 32 channel values.
	expected := []uint8{0, 8, 16, 24, 32, 41, 49, 57, 65, 74, 82, 90, 98, 106, 115, 123, 131, 139, 148, 156, 164, 172, 180, 189, 197, 205, 213, 222, 230, 238, 246, 255}
	raw := make([]byte, 32*3*2)
	for channel, shift := range []uint{10, 5, 0} {
		for value := 0; value < 32; value++ {
			binary.LittleEndian.PutUint16(raw[(channel*32+value)*2:], uint16(value)<<shift)
		}
	}
	screen, err := newRGB555Screen(1, 1, dec, 32)
	if err != nil {
		t.Fatal(err)
	}
	rect := make([]byte, 8)
	binary.LittleEndian.PutUint16(rect[4:], 32)
	binary.LittleEndian.PutUint16(rect[6:], 3)
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[1] = rect
	fields[2] = enc.EncodeAll(raw, nil)
	if _, err = screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	for channel := 0; channel < 3; channel++ {
		for x, want := range expected {
			pixel := screen.canvas.RGBAAt(x, channel)
			values := []uint8{pixel.R, pixel.G, pixel.B}
			for c, got := range values {
				expectedValue := uint8(0)
				if c == channel {
					expectedValue = want
				}
				if got != expectedValue {
					t.Fatalf("pixel (%d,%d) channel %d = %d, want %d", x, channel, c, got, expectedValue)
				}
			}
		}
	}
}
