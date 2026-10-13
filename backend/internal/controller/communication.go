package controller

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const maxFileSize = 50 * 1024 * 1024
const fileChunkSize = 32000

type chatMessage struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Text      string `json:"text"`
	Status    string `json:"status"`
}
type fileTransferState struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Acknowledged int64  `json:"acknowledged"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}
type outgoingFile struct {
	file    *os.File
	sent    int64
	pending []int64
}

func decodeUTF16LE(data []byte) (string, error) {
	if len(data)%2 != 0 {
		return "", errors.New("invalid UTF-16 message")
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(units)), nil
}

func (v *Viewer) appendMessageLocked(message chatMessage) {
	v.state.Messages = append(v.state.Messages, message)
	if len(v.state.Messages) > 100 {
		v.state.Messages = v.state.Messages[len(v.state.Messages)-100:]
	}
}

func (v *Viewer) chat(message string) error {
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) || strings.Contains(message, "|||") || len(utf16le(message)) > 4096 {
		return errors.New("message is empty, too long, or contains an unsupported delimiter")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.State != "connected" {
		return errors.New("session is not connected")
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	select {
	case v.commands <- cmdItem{payload: utf16le("12|||" + message), unicode: true, messageID: id}:
		v.appendMessageLocked(chatMessage{ID: id, Direction: "outgoing", Text: message, Status: "queued"})
		return nil
	default:
		return errors.New("input queue is full")
	}
}

func (v *Viewer) clearUploadLocked() {
	if v.upload == nil {
		return
	}
	name := v.upload.file.Name()
	v.upload.file.Close()
	os.Remove(name)
	v.upload = nil
}

func (v *Viewer) offerFile(file *os.File, name string, size int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.State != "connected" {
		return errors.New("session is not connected")
	}
	if v.upload != nil {
		return errors.New("another file transfer is active")
	}
	if size < 1 || size > maxFileSize || name == "" || strings.Contains(name, "|||") || strings.ContainsRune(name, 0) {
		return errors.New("invalid file name or size (maximum 50 MB; empty files are unsupported)")
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	payload := utf16le(fmt.Sprintf("13|||%s|||%s|||%d", id, name, size))
	if len(payload) > 4096 {
		return errors.New("file name is too long")
	}
	select {
	case v.commands <- cmdItem{payload: payload, unicode: true}:
		v.upload = &outgoingFile{file: file}
		v.state.Transfer = &fileTransferState{ID: id, Name: name, Size: size, Status: "waiting_for_acceptance"}
		return nil
	default:
		return errors.New("input queue is full")
	}
}

func (v *Viewer) cancelFile() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.upload == nil {
		return errors.New("no active file transfer")
	}
	select {
	case v.commands <- cmdItem{payload: utf16le("16|||" + v.state.Transfer.ID), unicode: true}:
		v.state.Transfer.Status = "canceled"
		v.clearUploadLocked()
		return nil
	default:
		return errors.New("input queue is full")
	}
}

// Original KeepSendingFile uses 1-based offsets and 32000-byte blocks.
func (v *Viewer) sendFileChunkLocked(session *controlSession) error {
	transfer, upload := v.state.Transfer, v.upload
	if upload.sent >= transfer.Size {
		return nil
	}
	data := make([]byte, min(int64(fileChunkSize), transfer.Size-upload.sent))
	if _, err := upload.file.ReadAt(data, upload.sent); err != nil {
		return err
	}
	payload := append([]byte(fmt.Sprintf("15|||%s|||%d|||", transfer.ID, upload.sent+1)), data...)
	if err := session.send(payload); err != nil {
		return err
	}
	upload.sent += int64(len(data))
	upload.pending = append(upload.pending, upload.sent)
	transfer.Status = "sending"
	return nil
}

func (v *Viewer) handleCommunication(session *controlSession, kind int, fields [][]byte) error {
	if len(fields) < 2 {
		return errors.New("truncated communication packet")
	}
	if kind == 12 {
		message, err := decodeUTF16LE(fields[1])
		if err != nil {
			return err
		}
		v.mu.Lock()
		v.appendMessageLocked(chatMessage{ID: strconv.FormatInt(time.Now().UnixNano(), 10), Direction: "incoming", Text: message, Status: "received"})
		v.mu.Unlock()
		return nil
	}
	id, err := ansiDecode(fields[1], session.codec)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.upload == nil || v.state.Transfer.ID != id {
		return nil
	}
	switch kind {
	case 14:
		if v.state.Transfer.Status != "waiting_for_acceptance" {
			return nil
		}
		if err := v.sendFileChunkLocked(session); err != nil {
			return err
		}
		return v.sendFileChunkLocked(session)
	case 21:
		if len(v.upload.pending) == 0 {
			return nil
		}
		v.state.Transfer.Acknowledged = v.upload.pending[0]
		v.upload.pending = v.upload.pending[1:]
		if v.state.Transfer.Acknowledged == v.state.Transfer.Size {
			v.state.Transfer.Status = "completed"
			v.clearUploadLocked()
			return nil
		}
		return v.sendFileChunkLocked(session)
	case 16:
		v.state.Transfer.Status = "canceled"
		v.clearUploadLocked()
	}
	return nil
}

func (v *Viewer) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+1024*1024)
	reader, err := r.MultipartReader()
	if err != nil {
		reply(w, 400, []byte(`{"error":"invalid multipart upload"}`), "application/json")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		reply(w, 400, []byte(`{"error":"file is required"}`), "application/json")
		return
	}
	defer part.Close()
	file, err := os.CreateTemp("", "ultraview-send-*")
	if err == nil {
		var size int64
		size, err = io.Copy(file, io.LimitReader(part, maxFileSize+1))
		if err == nil && size > maxFileSize {
			err = errors.New("file exceeds 50 MB")
		}
		if err == nil {
			err = v.offerFile(file, filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/")), size)
		}
		if err != nil {
			name := file.Name()
			file.Close()
			os.Remove(name)
		}
	}
	if err != nil {
		out, _ := json.Marshal(map[string]string{"error": err.Error()})
		reply(w, 400, out, "application/json")
		return
	}
	reply(w, 200, []byte(`{"ok":true}`), "application/json")
}
