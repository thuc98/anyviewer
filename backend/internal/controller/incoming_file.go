package controller

import (
	"encoding/binary"
	"errors"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type incomingFileState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Received int64  `json:"received"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}
type incomingFile struct {
	state *incomingFileState
	file  *os.File
}

func fileNumber(field []byte) (int64, error) {
	if len(field) != 8 {
		return 0, errors.New("invalid file numeric field")
	}
	value := math.Float64frombits(binary.LittleEndian.Uint64(field))
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value > maxFileSize+1 || math.Trunc(value) != value {
		return 0, errors.New("file size or offset outside supported range")
	}
	return int64(value), nil
}
func closeIncomingFile(file *incomingFile) {
	if file.file == nil {
		return
	}
	name := file.file.Name()
	file.file.Close()
	os.Remove(name)
	file.file = nil
}
func (v *Viewer) clearIncomingLocked(keepCompleted bool) {
	for _, file := range v.incoming {
		if keepCompleted && file.state.Status == "completed" {
			continue
		}
		if file.state.Status == "receiving" || file.state.Status == "offered" {
			file.state.Status = "failed"
			file.state.Error = "session disconnected"
		}
		closeIncomingFile(file)
	}
	if !keepCompleted {
		v.incoming = nil
	}
}
func (v *Viewer) acceptIncomingFile(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	file := v.incoming[id]
	if v.state.State != "connected" {
		return errors.New("session is not connected")
	}
	if file == nil || file.state.Status != "offered" {
		return errors.New("file offer is no longer available")
	}
	temp, err := os.CreateTemp("", "anyviewer-receive-*")
	if err != nil {
		return err
	}
	select {
	case v.commands <- cmdItem{payload: utf16le("14|||" + id), unicode: true}:
		file.file = temp
		file.state.Status = "receiving"
		return nil
	default:
		temp.Close()
		os.Remove(temp.Name())
		return errors.New("input queue is full")
	}
}
func (v *Viewer) cancelIncomingFile(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	file := v.incoming[id]
	if file == nil || (file.state.Status != "receiving" && file.state.Status != "offered") {
		return errors.New("no active incoming file")
	}
	if v.state.State != "connected" {
		return errors.New("session is not connected")
	}
	select {
	case v.commands <- cmdItem{payload: utf16le("16|||" + id), unicode: true}:
		closeIncomingFile(file)
		file.state.Status = "canceled"
		return nil
	default:
		return errors.New("input queue is full")
	}
}
func (v *Viewer) handleIncomingFile(session *controlSession, kind int, fields [][]byte) error {
	if len(fields) < 2 {
		return errors.New("truncated incoming file packet")
	}
	id, err := ansiDecode(fields[1], session.codec)
	if err != nil {
		return err
	}
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n") || strings.Contains(id, "|||") {
		return errors.New("invalid incoming transfer ID")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if kind == 13 {
		if len(fields) != 4 {
			return errors.New("invalid incoming file offer")
		}
		if v.incoming == nil {
			v.incoming = make(map[string]*incomingFile)
		}
		if v.incoming[id] != nil {
			return nil
		}
		if len(v.incoming) >= 10 {
			session.sendWithEncoding(utf16le("16|||"+id), true)
			return errors.New("too many incoming files; reconnect to clear completed files")
		}
		name, err := ansiDecode(fields[2], session.codec)
		if err != nil {
			return err
		}
		name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
		size, err := fileNumber(fields[3])
		if err != nil || size > maxFileSize {
			session.sendWithEncoding(utf16le("16|||"+id), true)
			return errors.New("incoming file exceeds 50 MB or has invalid size")
		}
		if name == "" || name == "." || name == "/" || len([]rune(name)) > 255 || strings.ContainsAny(name, "\x00\r\n") {
			return errors.New("invalid incoming filename")
		}
		state := &incomingFileState{ID: id, Name: name, Size: size, Status: "offered"}
		v.incoming[id] = &incomingFile{state: state}
		v.state.Incoming = append(v.state.Incoming, state)
		return nil
	}
	file := v.incoming[id]
	if file == nil {
		return nil
	}
	if kind == 16 {
		if file.state.Status == "offered" || file.state.Status == "receiving" {
			closeIncomingFile(file)
			file.state.Status = "canceled"
		}
		return nil
	}
	if kind != 15 || file.state.Status != "receiving" {
		return nil
	}
	fail := func(err error) error {
		file.state.Status = "failed"
		file.state.Error = err.Error()
		closeIncomingFile(file)
		session.sendWithEncoding(utf16le("16|||"+id), true)
		return err
	}
	if len(fields) != 4 {
		return fail(errors.New("invalid incoming chunk"))
	}
	offset, err := fileNumber(fields[2])
	if err != nil || offset != file.state.Received+1 {
		return fail(errors.New("incoming file chunk out of order"))
	}
	if len(fields[3]) == 0 || len(fields[3]) > fileChunkSize || file.state.Received+int64(len(fields[3])) > file.state.Size {
		return fail(errors.New("incoming chunk exceeds expected file size"))
	}
	if _, err := file.file.WriteAt(fields[3], file.state.Received); err != nil {
		return fail(err)
	}
	file.state.Received += int64(len(fields[3]))
	if file.state.Received == file.state.Size {
		if err := file.file.Sync(); err != nil {
			return fail(err)
		}
		file.state.Status = "completed"
	}
	return session.sendWithEncoding(utf16le("21|||"+id), true)
}
func (v *Viewer) downloadIncomingFile(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	incoming := v.incoming[r.URL.Query().Get("id")]
	if incoming == nil || incoming.state.Status != "completed" || incoming.file == nil {
		v.mu.Unlock()
		reply(w, 404, []byte(`{"error":"file is not ready for download"}`), "application/json")
		return
	}
	name := incoming.state.Name
	file, err := os.Open(incoming.file.Name())
	v.mu.Unlock()
	if err != nil {
		reply(w, 500, []byte(`{"error":"cannot open received file"}`), "application/json")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "cannot read file", 500)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
}
