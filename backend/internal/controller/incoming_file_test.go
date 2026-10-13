package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func wireFileNumber(value int64) []byte {
	field := make([]byte, 8)
	binary.LittleEndian.PutUint64(field, math.Float64bits(float64(value)))
	return field
}
func incomingTestSession(t *testing.T) (*controlSession, chan []byte) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { local.Close(); peer.Close() })
	packets := make(chan []byte, 10)
	go func() {
		for {
			var head [4]byte
			if _, err := io.ReadFull(peer, head[:]); err != nil {
				return
			}
			body := make([]byte, binary.LittleEndian.Uint32(head[:]))
			if _, err := io.ReadFull(peer, body); err != nil {
				return
			}
			packet, _ := decodeTransportBody(body)
			packets <- packet.data
		}
	}()
	session := newControlSession(time.Second, true)
	session.codec = "cp1252"
	session.route = &targetRoute{publicIP: "203.0.113.1", tcpPort: 1234}
	session.relay = newTransport(time.Second, true)
	session.relay.conn = local
	return session, packets
}
func TestIncomingFileReceiveAndProtectedDownload(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	session, acks := incomingTestSession(t)
	data := bytes.Repeat([]byte{0, 255, '|', 'x'}, 8003)
	id := "fixture-incoming"
	if err := v.handleIncomingFile(session, 13, [][]byte{{13}, []byte(id), []byte(`C:\folder\test.bin`), wireFileNumber(int64(len(data)))}); err != nil {
		t.Fatal(err)
	}
	if v.incoming[id].file != nil {
		t.Fatal("created file before acceptance")
	}
	if err := v.acceptIncomingFile(id); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	if !command.unicode || !bytes.Equal(command.payload, utf16le("14|||"+id)) {
		t.Fatal("wrong acceptance command")
	}
	tempName := v.incoming[id].file.Name()
	for offset := 0; offset < len(data); {
		end := min(offset+fileChunkSize, len(data))
		if err := v.handleIncomingFile(session, 15, [][]byte{{15}, []byte(id), wireFileNumber(int64(offset + 1)), data[offset:end]}); err != nil {
			t.Fatal(err)
		}
		if ack := <-acks; !bytes.Equal(ack, utf16le("21|||"+id)) {
			t.Fatal("wrong receiver acknowledgement")
		}
		offset = end
	}
	file := v.incoming[id]
	if file.state.Status != "completed" || file.state.Received != int64(len(data)) || file.state.Name != "test.bin" {
		t.Fatal("incoming transfer incomplete")
	}
	handler := v.handler([]byte("index"), "fixture-token")
	download := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/file/download?id="+id, nil)
		req.Host = "127.0.0.1:8765"
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("X-Viewer-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := download(""); rec.Code != 403 {
		t.Fatal("download exposed without token")
	}
	v.mu.Lock()
	v.clearIncomingLocked(true)
	v.mu.Unlock()
	rec := download("fixture-token")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download does not match received file")
	}
	v.shutdown()
	if _, err := os.Stat(tempName); !os.IsNotExist(err) {
		t.Fatal("received file was not cleaned up on shutdown")
	}
}

func TestIncomingOutOfOrderAndCancellationCleanup(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	session, acks := incomingTestSession(t)
	v.handleIncomingFile(session, 13, [][]byte{{13}, []byte("bad"), []byte("test.txt"), wireFileNumber(10)})
	v.acceptIncomingFile("bad")
	<-v.commands
	path := v.incoming["bad"].file.Name()
	if err := v.handleIncomingFile(session, 15, [][]byte{{15}, []byte("bad"), wireFileNumber(2), []byte("x")}); err == nil {
		t.Fatal("accepted out-of-order data")
	}
	<-acks
	if v.incoming["bad"].state.Status != "failed" {
		t.Fatal("invalid transfer not failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial file not removed")
	}
	v.handleIncomingFile(session, 13, [][]byte{{13}, []byte("cancel"), []byte("test.txt"), wireFileNumber(10)})
	if err := v.cancelIncomingFile("cancel"); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	if !bytes.Equal(command.payload, utf16le("16|||cancel")) || v.incoming["cancel"].state.Status != "canceled" {
		t.Fatal("offer cancellation failed")
	}
}
