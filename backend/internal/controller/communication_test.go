package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChatWireAndReception(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	if err := v.chat("hello"); err == nil {
		t.Fatal("accepted chat while disconnected")
	}
	v.state.State = "connected"
	text := "Xin chào 👋"
	if err := v.chat(text); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	if !command.unicode || !bytes.Equal(command.payload, utf16le("12|||"+text)) {
		t.Fatal("incorrect chat encoding")
	}
	if err := v.handleCommunication(&controlSession{}, 12, [][]byte{{12}, utf16le(text)}); err != nil {
		t.Fatal(err)
	}
	if len(v.state.Messages) != 2 || v.state.Messages[1].Text != text || v.state.Messages[1].Direction != "incoming" {
		t.Fatal("missing received message")
	}
	if err := v.chat("invalid|||delimiter"); err == nil {
		t.Fatal("accepted delimiter")
	}
}

func TestFileAcceptanceChunksAcknowledgementsAndCleanup(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	file, err := os.CreateTemp(t.TempDir(), "fixture-*")
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	data := bytes.Repeat([]byte{0, 255, '|', 42}, 24001)
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := v.offerFile(file, "test.bin", int64(len(data))); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	id := v.state.Transfer.ID
	if !command.unicode || !bytes.Equal(command.payload, utf16le("13|||"+id+"|||test.bin|||"+strconv.Itoa(len(data)))) {
		t.Fatal("invalid file offer")
	}
	if v.upload.sent != 0 {
		t.Fatal("sent data before acceptance")
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	packets := make(chan *transportPacket, 10)
	go func() {
		for {
			var prefix [4]byte
			if _, err := io.ReadFull(remote, prefix[:]); err != nil {
				return
			}
			body := make([]byte, binary.LittleEndian.Uint32(prefix[:]))
			if _, err := io.ReadFull(remote, body); err != nil {
				return
			}
			packet, _ := decodeTransportBody(body)
			packets <- packet
		}
	}()
	session := newControlSession(time.Second, true)
	session.codec = "cp1252"
	session.route = &targetRoute{publicIP: "203.0.113.1", tcpPort: 1234}
	session.relay = newTransport(time.Second, true)
	session.relay.conn = local
	fields := [][]byte{{14}, []byte(id)}
	if err := v.handleCommunication(session, 14, fields); err != nil {
		t.Fatal(err)
	}
	sent := 0
	checkChunk := func() {
		t.Helper()
		packet := <-packets
		expectedSize := min(fileChunkSize, len(data)-sent)
		header := []byte("15|||" + id + "|||" + strconv.Itoa(sent+1) + "|||")
		if packet.flag || !bytes.Equal(packet.data, append(header, data[sent:sent+expectedSize]...)) {
			t.Fatal("invalid file chunk or 1-based offset")
		}
		sent += expectedSize
	}
	checkChunk()
	checkChunk()
	for ack := 0; ack < 4; ack++ {
		if err := v.handleCommunication(session, 21, [][]byte{{21}, []byte(id)}); err != nil {
			t.Fatal(err)
		}
		if ack < 2 {
			checkChunk()
		}
		if ack < 3 && v.state.Transfer.Status == "completed" {
			t.Fatal("completed before final receiver acknowledgement")
		}
	}
	if sent != len(data) || v.state.Transfer.Status != "completed" || v.state.Transfer.Acknowledged != int64(len(data)) || v.upload != nil {
		t.Fatal("transfer did not complete")
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("temporary file was not removed")
	}
}

func TestFileCancellation(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	file, err := os.CreateTemp(t.TempDir(), "fixture-*")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("test"))
	if err := v.offerFile(file, "test.txt", 4); err != nil {
		t.Fatal(err)
	}
	<-v.commands
	if err := v.cancelFile(); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	if !bytes.Equal(command.payload, utf16le("16|||"+v.state.Transfer.ID)) || !command.unicode || v.upload != nil || v.state.Transfer.Status != "canceled" {
		t.Fatal("file cancellation failed")
	}
}

func TestCommunicationHTTP(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	handler := v.handler([]byte("const token='__TOKEN__';"), "fixture-token")
	request := func(path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, body)
		req.Host = "127.0.0.1:8765"
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("X-Viewer-Token", "fixture-token")
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("/chat", strings.NewReader(`{"message":"test"}`), "application/json"); rec.Code != 400 {
		t.Fatal("chat accepted offline")
	}
	v.state.State = "connected"
	if rec := request("/chat", strings.NewReader(`{"message":"Xin chào"}`), "application/json"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	<-v.commands
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("real file bytes"))
	writer.Close()
	if rec := request("/file", &body, writer.FormDataContentType()); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if v.upload == nil || v.state.Transfer.Status != "waiting_for_acceptance" {
		t.Fatal("upload wasn't registered")
	}
	<-v.commands
	if rec := request("/file/cancel", strings.NewReader(`{}`), "application/json"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if v.upload != nil {
		t.Fatal("upload not cleaned up")
	}
}
