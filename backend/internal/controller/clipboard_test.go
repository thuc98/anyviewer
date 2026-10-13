package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/klauspost/compress/zstd"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClipboardWireFormats(t *testing.T) {
	text := "Xin chào 👋"
	payload, unicodeBytes, err := clipboardCommand(text)
	if err != nil || !unicodeBytes || !bytes.Equal(payload, utf16le("5|||"+text)) {
		t.Fatal("incorrect legacy clipboard command")
	}
	for _, text := range []string{"a|||b", strings.Repeat("văn bản 👋\n", 100)} {
		payload, unicodeBytes, err := clipboardCommand(text)
		if err != nil {
			t.Fatal(err)
		}
		if unicodeBytes {
			t.Fatal("compressed clipboard must use binary relay flag")
		}
		parts := bytes.SplitN(payload, []byte("|||"), 3)
		if len(parts) != 3 || string(parts[0]) != "33" {
			t.Fatal("incorrect compressed clipboard header")
		}
		dec, err := zstd.NewReader(nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := dec.DecodeAll(parts[2], nil)
		dec.Close()
		if err != nil || !bytes.Equal(raw, utf16le(text)) {
			t.Fatal("clipboard compression mismatch")
		}
	}
}

func TestClipboardReceiveEchoAndDisable(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	text := "Xin chào 👋"
	fields := [][]byte{{5}, utf16le(text)}
	if err := v.receiveClipboard(5, fields); err != nil {
		t.Fatal(err)
	}
	if v.clipboard.Version != 0 {
		t.Fatal("received clipboard while disabled")
	}
	v.settings.ClipboardSync = true
	v.state.State = "connected"
	if err := v.receiveClipboard(5, fields); err != nil {
		t.Fatal(err)
	}
	if v.clipboard.Text != text || v.clipboard.Version != 1 {
		t.Fatal("clipboard wasn't received")
	}
	v.receiveClipboard(5, fields)
	if v.clipboard.Version != 1 {
		t.Fatal("duplicate clipboard wasn't suppressed")
	}
	if err := v.sendClipboard("local text"); err != nil {
		t.Fatal(err)
	}
	if err := v.receiveClipboard(5, [][]byte{{5}, utf16le("local text")}); err != nil {
		t.Fatal(err)
	}
	if v.clipboard.Version != 1 {
		t.Fatal("local clipboard echo wasn't suppressed")
	}
	v.receiveClipboard(5, [][]byte{{5}, utf16le("remote change")})
	if v.clipboard.Version != 2 {
		t.Fatal("new remote clipboard wasn't received")
	}
}

func TestCompressedClipboardLimits(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.settings.ClipboardSync = true
	text := strings.Repeat("clipboard|||🙂", 100)
	payload, _, err := clipboardCommand(text)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.SplitN(payload, []byte("|||"), 3)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(utf16le(text))))
	if err := v.receiveClipboard(33, [][]byte{{33}, size, parts[2]}); err != nil {
		t.Fatal(err)
	}
	if v.clipboard.Text != text {
		t.Fatal("compressed UTF-16 wasn't decoded")
	}
	before := v.clipboard
	binary.LittleEndian.PutUint32(size, maxClipboardBytes+2)
	if err := v.receiveClipboard(33, [][]byte{{33}, size, parts[2]}); err == nil {
		t.Fatal("accepted oversized decompression")
	}
	if v.clipboard != before {
		t.Fatal("invalid clipboard changed state")
	}
	for _, value := range []string{"", "bad\x00text", strings.Repeat("x", maxClipboardBytes)} {
		if _, _, err := clipboardCommand(value); err == nil {
			t.Fatal("accepted invalid clipboard")
		}
	}
}

func TestClipboardHTTPProtectionAndLargeText(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	v.settings.ClipboardSync = true
	handler := v.handler([]byte("index"), "fixture-token")
	request := func(method, path, token string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body)
		req.Host = "127.0.0.1:8765"
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("X-Viewer-Token", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET", "/clipboard", "", nil); rec.Code != 403 {
		t.Fatal("clipboard exposed without token")
	}
	text := strings.Repeat("test clipboard ", 1000)
	body, _ := json.Marshal(map[string]string{"text": text})
	if rec := request("POST", "/clipboard", "fixture-token", bytes.NewReader(body)); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	v.receiveClipboard(5, [][]byte{{5}, utf16le("remote text")})
	rec := request("GET", "/clipboard", "fixture-token", nil)
	var data clipboardState
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil || data.Text != "remote text" {
		t.Fatal("remote clipboard wasn't available to authorized client")
	}
	if rec := request("GET", "/status", "", nil); bytes.Contains(rec.Body.Bytes(), []byte("remote text")) {
		t.Fatal("clipboard leaked into ordinary status response")
	}
}
