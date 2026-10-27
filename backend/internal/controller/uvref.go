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

func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16Units(s) {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], r)
		out = append(out, b[0], b[1])
	}
	return out
}

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

func encodeTransportFrame(body []byte) ([]byte, error) {
	if len(body) > 0x7fffffff-4 {
		return nil, errors.New("frame exceeds checked managed allocation size")
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

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

type transportPacket struct {
	kind         string
	flag         bool
	addressValue uint32
	port         uint16
	data         []byte
}

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

func deriveVFunctionKeyBytes(password string, length int) []byte {
	sum := sha1.Sum(utf16le(password))
	out := make([]byte, length)
	copy(out, sum[:])
	return out
}

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
	metadata := strings.Split(terms[10], "|")
	if len(metadata) != 6 {
		return nil, errors.New("term 10 requires six unsigned decimal metadata fields")
	}
	for _, value := range metadata {
		if !isDecimal(value) {
			return nil, errors.New("term 10 requires six unsigned decimal metadata fields")
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

type atlasTile struct {
	x, y, w, h, sx, sy int
}

func atlasLayout(rectangles [][4]int, blockSize, atlasHeight int) ([]atlasTile, error) {
	return packAtlas(rectangles, blockSize, atlasHeight, false)
}

func packAtlas(rectangles [][4]int, blockSize, atlasHeight int, compactColumns bool) ([]atlasTile, error) {
	if blockSize <= 0 || atlasHeight <= 0 {
		return nil, errors.New("invalid atlas geometry")
	}
	var tiles []atlasTile
	atlasY := 0
	atlasX, columnWidth := 0, 0
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
					if compactColumns {
						atlasX += columnWidth
					} else {
						atlasX += blockSize
					}
					columnWidth = 0
				}
				columnWidth = max(columnWidth, tileWidth)
				tiles = append(tiles, atlasTile{
					x: x + dx, y: y + dy, w: tileWidth, h: tileHeight,
					sx: atlasX, sy: atlasY,
				})
				atlasY += tileHeight
			}
		}
	}
	return tiles, nil
}
