// Portable protocol models recovered from UltraViewer 6.6.133.
// Go port of uv_reference.py: only valid, non-overlapping buffers are modeled;
// malformed input returns an error rather than being silently accepted.
package controller

import (
	"crypto/cipher"
	"crypto/des"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// ---- ANSI (Windows code page) helpers ------------------------------------

// ansiEncode encodes a string with the given ANSI codec (only cp1252 is used by
// the shipped profiles). Unmappable runes are rejected, matching errors="strict".
func ansiEncode(s, codec string) ([]byte, error) {
	switch strings.ToLower(codec) {
	case "cp1252", "windows-1252":
		return charmap.Windows1252.NewEncoder().Bytes([]byte(s))
	case "latin-1", "iso-8859-1", "cp819":
		return charmap.ISO8859_1.NewEncoder().Bytes([]byte(s))
	default:
		return nil, fmt.Errorf("unsupported ansi codec %q", codec)
	}
}

// ansiDecode decodes ANSI bytes to a string (strict).
func ansiDecode(b []byte, codec string) (string, error) {
	switch strings.ToLower(codec) {
	case "cp1252", "windows-1252":
		out, err := charmap.Windows1252.NewDecoder().Bytes(b)
		return string(out), err
	case "latin-1", "iso-8859-1", "cp819":
		out, err := charmap.ISO8859_1.NewDecoder().Bytes(b)
		return string(out), err
	default:
		return "", fmt.Errorf("unsupported ansi codec %q", codec)
	}
}

// utf16le encodes a string as UTF-16LE with no BOM, like Windows BSTR bytes.
func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16Units(s) {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], r)
		out = append(out, b[0], b[1])
	}
	return out
}

// utf16Units returns the UTF-16 code units for a string.
func utf16Units(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for _, r := range s {
		if r <= 0xFFFF {
			units = append(units, uint16(r))
			continue
		}
		r -= 0x10000
		units = append(units, 0xD800+uint16(r>>10), 0xDC00+uint16(r&0x3FF))
	}
	return units
}

// ---- byte-array field framing (R200) -------------------------------------

// encodeByteArrayFields lays out each field as LE uint32 length + value.
func encodeByteArrayFields(fields [][]byte) []byte {
	out := make([]byte, 0)
	var l [4]byte
	for _, f := range fields {
		binary.LittleEndian.PutUint32(l[:], uint32(len(f)))
		out = append(out, l[:]...)
		out = append(out, f...)
	}
	return out
}

// decodeByteArrayFields parses a complete field payload; truncation is rejected.
func decodeByteArrayFields(payload []byte) ([][]byte, error) {
	fields := [][]byte{}
	off := 0
	for off < len(payload) {
		if len(payload)-off < 4 {
			return nil, errors.New("truncated field length")
		}
		length := int(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
		if length > len(payload)-off {
			return nil, errors.New("truncated field value")
		}
		v := make([]byte, length)
		copy(v, payload[off:off+length])
		fields = append(fields, v)
		off += length
	}
	return fields, nil
}

// ---- transport framing ---------------------------------------------------

// encodeTransportFrame prepends an Int32 LE length to the body (E119/e31).
func encodeTransportFrame(body []byte) ([]byte, error) {
	if len(body) > 0x7fffffff-4 {
		return nil, errors.New("frame exceeds checked managed allocation size")
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

// encodeRelayFrame builds the mode0 relay frame (RC40 de1): [0, flag, addr, port]
// + payload, then length-prefixed. The IPv4 value is a big-endian integer stored
// little-endian, exactly reproducing struct.pack("<BBIH", 0, flag, address, port).
func encodeRelayFrame(payload []byte, ipv4 string, port, flag int) ([]byte, error) {
	parts := strings.Split(ipv4, ".")
	if len(parts) != 4 {
		return nil, errors.New("expected four decimal IPv4 octets")
	}
	var octets [4]int
	for i, s := range parts {
		if s == "" {
			return nil, errors.New("expected four decimal IPv4 octets")
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 255 {
			return nil, errors.New("IPv4 octet out of range")
		}
		octets[i] = n
	}
	if port < 0 || port > 65535 {
		return nil, errors.New("port out of range")
	}
	if flag < 0 || flag > 255 {
		return nil, errors.New("flag out of range")
	}
	address := uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])
	head := make([]byte, 8)
	head[0] = 0
	head[1] = byte(flag)
	binary.LittleEndian.PutUint32(head[2:6], address)
	binary.LittleEndian.PutUint16(head[6:8], uint16(port))
	return encodeTransportFrame(append(head, payload...))
}

// transportPacket is the decoded e37 transport dispatch result.
type transportPacket struct {
	kind         string // "relay" or "direct"
	flag         bool
	addressValue uint32
	port         uint16
	data         []byte
}

// decodeTransportBody decodes the transport dispatch header; nil result + nil
// error means an unknown kind (mirrors the Python None return).
func decodeTransportBody(body []byte) (*transportPacket, error) {
	if len(body) == 0 {
		return nil, errors.New("empty transport body")
	}
	switch body[0] {
	case 0:
		if len(body) < 8 {
			return nil, errors.New("truncated relay header")
		}
		return &transportPacket{
			kind:         "relay",
			flag:         body[1] != 0,
			addressValue: binary.LittleEndian.Uint32(body[2:6]),
			port:         binary.LittleEndian.Uint16(body[6:8]),
			data:         append([]byte(nil), body[8:]...),
		}, nil
	case 1:
		return &transportPacket{kind: "direct", data: append([]byte(nil), body[1:]...)}, nil
	}
	return nil, nil
}

// ---- crypto (registration TripleDES) -------------------------------------

// deriveVFunctionKeyBytes = SHA1(UTF16LE(password)) resized by zero extension or
// truncation (RC40 1485).
func deriveVFunctionKeyBytes(password string, length int) []byte {
	sum := sha1.Sum(utf16le(password))
	out := make([]byte, length)
	copy(out, sum[:])
	return out
}

// registrationPassword = startup VFunction key (E124): chr(5,10,15,20,25).
var registrationPassword = string([]byte{5, 10, 15, 20, 25})

func pkcs7Pad(data []byte, block int) []byte {
	n := block - len(data)%block
	pad := make([]byte, n)
	for i := range pad {
		pad[i] = byte(n)
	}
	return append(append([]byte(nil), data...), pad...)
}

func pkcs7Unpad(data []byte, block int) ([]byte, error) {
	if len(data) == 0 || len(data)%block != 0 {
		return nil, errors.New("invalid padded length")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > block || n > len(data) {
		return nil, errors.New("invalid PKCS7 padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("invalid PKCS7 padding")
		}
	}
	return data[:len(data)-n], nil
}

// encryptRegistrationBytes applies .NET TripleDES/CBC/PKCS7 defaults (RC40 1486).
func encryptRegistrationBytes(payload []byte) ([]byte, error) {
	key := deriveVFunctionKeyBytes(registrationPassword, 24)
	iv := deriveVFunctionKeyBytes("", 8)
	block, err := des.NewTripleDESCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(payload, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

// ---- HTTP tunnel preface -------------------------------------------------

// httpServicePreface builds the recovered Windows HTTP tunnel preface (e25/e24).
// The content length is copied from the artifact, not calculated.
func httpServicePreface(host string) []byte {
	lines := []string{
		"POST /UVService/ HTTP/1.1",
		"User-Agent: Mozilla/4.0 (compatible; MSIE5.01; Windows NT)",
		"Content-Type: application/octet-stream",
		"Connection: Keep-Alive",
		"Content-length: 8589934590",
		"Host: " + host,
		"",
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// ---- command 3 identity --------------------------------------------------

type command3Identity struct {
	clientID         int
	publicSourcePort int
	publicIP         string
}

func decodeCommandFields(payload []byte, codec string) ([]string, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty byte-array behavior is not reconstructed")
	}
	text, err := ansiDecode(payload, codec)
	if err != nil {
		return nil, err
	}
	return strings.Split(text, ":"), nil
}

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// extractCommand3Identity parses the identity/endpoint subset of command 3 (E131).
func extractCommand3Identity(payload []byte, codec string) (command3Identity, error) {
	var id command3Identity
	fields, err := decodeCommandFields(payload, codec)
	if err != nil {
		return id, err
	}
	if len(fields) < 4 || fields[0] != "3" {
		return id, errors.New("expected command 3 with endpoint and ID fields")
	}
	port, identity := fields[1], fields[2]
	if !isDecimal(port) || !isDecimal(identity) {
		return id, errors.New("expected ASCII decimal numeric fields")
	}
	if len(port) > 5 || len(identity) > 10 {
		return id, errors.New("numeric field exceeds modeled range")
	}
	portNumber, _ := strconv.Atoi(port)
	idNumber, _ := strconv.Atoi(identity)
	if portNumber < 0 || portNumber > 65535 || idNumber < 1 || idNumber > 2147483647 {
		return id, errors.New("numeric field outside modeled range")
	}
	return command3Identity{clientID: idNumber, publicSourcePort: portNumber, publicIP: fields[3]}, nil
}

// ---- command builders ----------------------------------------------------

// buildRegistrationCommand validates and joins the 26-term InitMyPassword
// command (E147).
func buildRegistrationCommand(terms []string, codec string) ([]byte, error) {
	if len(terms) != 26 {
		return nil, errors.New("registration requires exactly 26 string terms")
	}
	for _, chk := range []struct {
		i   int
		exp string
	}{{0, "3"}, {3, "0"}, {5, "0"}, {12, ""}} {
		if terms[chk.i] != chk.exp {
			return nil, fmt.Errorf("term %d must equal %q", chk.i, chk.exp)
		}
	}
	for i, v := range terms {
		if strings.ContainsRune(v, 0) || (i != 22 && strings.ContainsRune(v, ':')) {
			return nil, fmt.Errorf("unsupported delimiter or NUL in term %d", i)
		}
	}
	ticks := strings.Split(terms[22], ":")
	if len(ticks) != 3 {
		return nil, errors.New("term 22 requires three unsigned decimal tick strings")
	}
	for _, t := range ticks {
		if !isDecimal(t) {
			return nil, errors.New("term 22 requires three unsigned decimal tick strings")
		}
	}
	return ansiEncode(strings.Join(terms, ":"), codec)
}

// connectionRequestPasswordValue: type 0 hashes ANSI(32 spaces + password) as
// lowercase MD5 hex; other types reuse the string (0x6edc60, E162).
func connectionRequestPasswordValue(password string, passwordType int, codec string) (string, error) {
	if passwordType == 0 {
		b, err := ansiEncode(strings.Repeat(" ", 32)+password, codec)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%x", md5.Sum(b)), nil
	}
	return password, nil
}

// buildConnectionRequestCommand joins the 17 explicit command8 terms (E163/E165).
func buildConnectionRequestCommand(terms []string, codec string) ([]byte, error) {
	if len(terms) != 17 {
		return nil, errors.New("command8 requires 17 string terms")
	}
	if terms[0] != "8" {
		return nil, errors.New("command8 must begin with 8")
	}
	for _, t := range terms {
		if strings.ContainsRune(t, ':') || strings.ContainsRune(t, 0) {
			return nil, errors.New("command8 terms must not contain colon or NUL")
		}
	}
	return ansiEncode(strings.Join(terms, ":"), codec)
}

// loginNumericSaltDigest: type0 Login MD5(ANSI(eight-digit salt + password)).
func loginNumericSaltDigest(password, salt, codec string) (string, error) {
	if len(salt) != 8 || !isDecimal(salt) {
		return "", errors.New("Login salt must contain eight ASCII digits")
	}
	b, err := ansiEncode(salt+password, codec)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", md5.Sum(b)), nil
}

type loginPasswordFields struct {
	salt      string
	primary   string
	secondary string
}

// computeLoginPasswordFields reproduces login 6d6f3a..6d726e (R191).
func computeLoginPasswordFields(password string, passwordType int, salt, codec string) (loginPasswordFields, error) {
	var lf loginPasswordFields
	if passwordType == 0 {
		if salt == "" {
			return lf, errors.New("type0 login requires an explicit eight-digit salt")
		}
		primary, err := loginNumericSaltDigest(password, salt, codec)
		if err != nil {
			return lf, err
		}
		secondary, err := connectionRequestPasswordValue(password, 0, codec)
		if err != nil {
			return lf, err
		}
		return loginPasswordFields{salt: salt, primary: primary, secondary: secondary}, nil
	}
	return loginPasswordFields{salt: strings.Repeat(" ", 32), primary: password, secondary: password}, nil
}

// buildLoginCommand: "login:" + 22 preformatted fields, raw BSTR UTF16LE (E185).
func buildLoginCommand(fields []string) ([]byte, error) {
	if len(fields) != 22 {
		return nil, errors.New("login requires exactly 22 fields after its prefix")
	}
	for _, v := range fields {
		if strings.ContainsRune(v, ':') || strings.ContainsRune(v, 0) {
			return nil, errors.New("login fields cannot contain colon or NUL")
		}
	}
	return utf16le("login:" + strings.Join(fields, ":")), nil
}

// buildLoginWithPassword fills the sensitive login positions (0, 1, 12) in a
// caller-supplied 22-field schema; the input slice is not mutated.
func buildLoginWithPassword(fields []string, password string, passwordType int, salt, codec string) ([]byte, error) {
	if len(fields) != 22 {
		return nil, errors.New("login requires exactly 22 fields after its prefix")
	}
	values := append([]string(nil), fields...)
	auth, err := computeLoginPasswordFields(password, passwordType, salt, codec)
	if err != nil {
		return nil, err
	}
	values[0], values[1], values[12] = auth.salt, auth.primary, auth.secondary
	return buildLoginCommand(values)
}

// ---- JPEG atlas tile layout ----------------------------------------------

type atlasTile struct {
	x, y, w, h, sx, sy int
}

// atlasLayout yields tiles in observed order (screen_x,screen_y,w,h,atlas_x,atlas_y).
func atlasLayout(rectangles [][4]int, blockSize, atlasHeight int) ([]atlasTile, error) {
	if blockSize <= 0 || atlasHeight <= 0 {
		return nil, errors.New("invalid atlas geometry")
	}
	var tiles []atlasTile
	atlasY := 0
	column := 0
	for _, r := range rectangles {
		x, y, width, height := r[0], r[1], r[2], r[3]
		if width < 0 || height < 0 {
			return nil, errors.New("negative rectangle dimensions")
		}
		for dx := 0; dx < width; dx += blockSize {
			tileWidth := min(blockSize, width-dx)
			for dy := 0; dy < height; dy += blockSize {
				tileHeight := min(blockSize, height-dy)
				if tileHeight > atlasHeight {
					return nil, errors.New("tile cannot fit atlas height")
				}
				if atlasY+tileHeight > atlasHeight {
					atlasY = 0
					column++
				}
				tiles = append(tiles, atlasTile{
					x: x + dx, y: y + dy, w: tileWidth, h: tileHeight,
					sx: column * blockSize, sy: atlasY,
				})
				atlasY += tileHeight
			}
		}
	}
	return tiles, nil
}
