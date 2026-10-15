package controller

import (
	"encoding/binary"
	"errors"
	"image"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type shareCapabilities struct {
	Screen    bool `json:"screen"`
	Keyboard  bool `json:"keyboard"`
	Mouse     bool `json:"mouse"`
	Clipboard bool `json:"clipboard"`
}

var browserShareCapabilities = shareCapabilities{Screen: true}

type shareInput struct {
	Type   string
	X, Y   int
	Button string
	Down   bool
	VK     int
	System bool
	Delta  int
}

type shareControlTarget interface {
	Input(shareInput) error
	SetClipboard(string) error
	ReleaseInputs() error
}

type shareControls struct {
	capabilities shareCapabilities
	target       shareControlTarget
}

func (s *shareControls) handleInput(payload []byte) error {
	if !s.capabilities.Keyboard && !s.capabilities.Mouse {
		return errors.New("sharing source does not allow remote input")
	}
	event, err := decodeShareInput(payload)
	if err != nil {
		return err
	}
	if (event.Type == "key" && !s.capabilities.Keyboard) || (event.Type != "key" && !s.capabilities.Mouse) {
		return errors.New("sharing source does not allow this input")
	}
	if s.target == nil {
		return errors.New("sharing input adapter is unavailable")
	}
	return s.target.Input(event)
}

func (s *shareControls) handleClipboard(kind int, fields [][]byte) error {
	if !s.capabilities.Clipboard {
		return errors.New("sharing source does not allow clipboard access")
	}
	text, err := decodeClipboardPacket(kind, fields)
	if err != nil {
		return err
	}
	if s.target == nil {
		return errors.New("sharing clipboard adapter is unavailable")
	}
	return s.target.SetClipboard(text)
}

func (s *shareControls) close() error {
	if s.target != nil && (s.capabilities.Keyboard || s.capabilities.Mouse) {
		return s.target.ReleaseInputs()
	}
	return nil
}

func decodeShareInput(payload []byte) (shareInput, error) {
	var event shareInput
	if len(payload) > 128 {
		return event, errors.New("input command exceeds limit")
	}
	parts := strings.Split(string(payload), "|||")
	if len(parts) < 2 || parts[0] != "8" {
		return event, errors.New("invalid input command")
	}
	values := make([]int, len(parts)-1)
	for i, part := range parts[1:] {
		value, err := strconv.ParseInt(part, 10, 32)
		if err != nil {
			return event, errors.New("invalid input value")
		}
		values[i] = int(value)
	}
	switch values[0] {
	case 256, 257, 260, 261:
		if len(values) != 2 || values[1] < 0 || values[1] > 255 {
			return event, errors.New("invalid keyboard command")
		}
		event = shareInput{Type: "key", VK: values[1], Down: values[0] == 256 || values[0] == 260, System: values[0] >= 260}
	case 512:
		if len(values) != 4 || values[1] != 0 || values[2] < -32768 || values[2] > 32767 || values[3] < -32768 || values[3] > 32767 {
			return event, errors.New("invalid mouse position")
		}
		event = shareInput{Type: "move", X: values[2], Y: values[3]}
	case 513, 514, 516, 517, 519, 520:
		if len(values) != 1 {
			return event, errors.New("invalid mouse button command")
		}
		buttons := map[int]string{513: "left", 514: "left", 516: "right", 517: "right", 519: "middle", 520: "middle"}
		event = shareInput{Type: "button", Button: buttons[values[0]], Down: values[0] == 513 || values[0] == 516 || values[0] == 519}
	case 522:
		if len(values) != 2 || values[1]&0xffff != 0 {
			return event, errors.New("invalid mouse wheel command")
		}
		event = shareInput{Type: "wheel", Delta: values[1] >> 16}
	default:
		return event, errors.New("unsupported input command")
	}
	return event, nil
}

func encodeShareScreen(src image.Image) ([]byte, error) {
	if src == nil {
		return nil, errors.New("missing sharing frame")
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 || w*h > 16*1024*1024 {
		return nil, errors.New("sharing frame exceeds allocation policy")
	}
	raw := make([]byte, w*h*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			word := uint16(r>>11)<<10 | uint16(g>>11)<<5 | uint16(b>>11)
			binary.LittleEndian.PutUint16(raw[(y*w+x)*2:], word)
		}
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, err
	}
	defer encoder.Close()
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[1] = make([]byte, 8)
	binary.LittleEndian.PutUint16(fields[1][4:], uint16(w))
	binary.LittleEndian.PutUint16(fields[1][6:], uint16(h))
	fields[2] = encoder.EncodeAll(raw, nil)
	return encodeByteArrayFields(fields), nil
}
