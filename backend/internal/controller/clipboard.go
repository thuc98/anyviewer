package controller

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"strings"
	"unicode/utf8"
)

const maxClipboardBytes = 64 * 1024

type clipboardState struct {
	Version uint64 `json:"version"`
	Text    string `json:"text"`
	Error   string `json:"error,omitempty"`
}

func clipboardCommand(text string) ([]byte, bool, error) {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, false, errors.New("invalid clipboard text")
	}
	raw := utf16le(text)
	if len(raw) == 0 || len(raw) > maxClipboardBytes {
		return nil, false, errors.New("clipboard must contain text up to 64 KB (UTF-16LE)")
	}
	if len(raw) <= 150 && !strings.Contains(text, "|||") {
		return utf16le("5|||" + text), true, nil
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(10)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, false, err
	}
	defer enc.Close()
	compressed := enc.EncodeAll(raw, nil)
	return append([]byte(fmt.Sprintf("33|||%d|||", len(raw))), compressed...), false, nil
}

func (v *Viewer) sendClipboard(text string) error {
	payload, unicodeBytes, err := clipboardCommand(text)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.State != "connected" {
		return errors.New("session is not connected")
	}
	if !v.settings.ClipboardSync {
		return errors.New("clipboard synchronization is disabled")
	}
	if text == v.clipboardSent {
		return nil
	}
	select {
	case v.commands <- cmdItem{payload: payload, unicode: unicodeBytes}:
		v.clipboardSent = text
		return nil
	default:
		return errors.New("input queue is full")
	}
}

func (v *Viewer) receiveClipboard(kind int, fields [][]byte) error {
	v.mu.Lock()
	enabled := v.settings.ClipboardSync
	v.mu.Unlock()
	if !enabled {
		return nil
	}
	text, err := decodeClipboardPacket(kind, fields)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.settings.ClipboardSync {
		return nil
	}
	v.clipboard.Error = ""
	if text != v.clipboard.Text && text != v.clipboardSent {
		v.clipboard.Text = text
		v.clipboard.Version++
	}
	return nil
}

func decodeClipboardPacket(kind int, fields [][]byte) (string, error) {
	var raw []byte
	if kind == 5 {
		if len(fields) != 2 {
			return "", errors.New("invalid text clipboard packet")
		}
		raw = fields[1]
	} else if kind == 33 {
		if len(fields) != 3 || len(fields[1]) != 4 {
			return "", errors.New("invalid compressed clipboard packet")
		}
		size := int(binary.LittleEndian.Uint32(fields[1]))
		if size < 1 || size > maxClipboardBytes || size%2 != 0 {
			return "", errors.New("clipboard exceeds allocation policy")
		}
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1024*1024), zstd.WithDecoderMaxWindow(1024*1024))
		if err != nil {
			return "", err
		}
		raw, err = dec.DecodeAll(fields[2], make([]byte, 0, size))
		dec.Close()
		if err != nil || len(raw) != size {
			return "", errors.New("clipboard decompression failed or size differs")
		}
	} else {
		return "", errors.New("unsupported clipboard packet")
	}
	if len(raw) == 0 || len(raw) > maxClipboardBytes {
		return "", errors.New("clipboard exceeds allocation policy")
	}
	text, err := decodeUTF16LE(raw)
	if err != nil {
		return "", err
	}
	if strings.ContainsRune(text, 0) {
		return "", errors.New("clipboard text contains NUL")
	}
	return text, nil
}
