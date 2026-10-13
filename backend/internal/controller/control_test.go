package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func readControl(t *testing.T, ctx context.Context, conn *websocket.Conn, kind string) map[string]json.RawMessage {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(data, &message) != nil {
			t.Fatal("invalid message")
		}
		var value string
		json.Unmarshal(message["type"], &value)
		if value == kind {
			return message
		}
	}
}
func TestWebSocketControlStatusInputHeartbeatAndRelease(t *testing.T) {
	v, server := streamTestServer(t)
	var heartbeats atomic.Int32
	v.onHeartbeat = func() bool { heartbeats.Add(1); return true }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/stream/control", &websocket.DialOptions{Subprotocols: []string{"viewer", "test-token", "session"}})
	if err != nil {
		t.Fatal(err)
	}
	readControl(t, ctx, conn, "status")
	v.setState("connected")
	message := readControl(t, ctx, conn, "status")
	var state viewerState
	json.Unmarshal(message["status"], &state)
	if state.State != "connected" {
		t.Fatal("status change not pushed")
	}
	for _, data := range []string{`{"type":"heartbeat"}`, `{"type":"input","id":1,"event":{"type":"key","vk":65,"down":true}}`, `{"type":"input","id":2,"event":{"type":"key","vk":65,"down":false}}`} {
		if err = conn.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint64{1, 2} {
		message = readControl(t, ctx, conn, "input_ack")
		var actual uint64
		json.Unmarshal(message["id"], &actual)
		if actual != id || len(message["error"]) > 0 {
			t.Fatal("input order or acknowledgement invalid")
		}
	}
	if heartbeats.Load() != 1 {
		t.Fatal("heartbeat not handled")
	}
	if first, second := <-v.commands, <-v.commands; !first.down || second.down {
		t.Fatal("key down/up reordered")
	}
	conn.CloseNow()
	select {
	case <-v.releaseInputs:
	case <-ctx.Done():
		t.Fatal("control loss did not release held input")
	}
}
func TestVP8FailureLogsFFmpegStderr(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf \"Unknown encoder 'libvpx'\\n\" >&2\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	v, _ := streamTestServer(t)
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "screen", "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = v.pumpVideo(ctx, track, make(chan videoFeedback), make(chan struct{}), "vp8-failure-test")
	if err == nil {
		t.Fatal("fake encoder succeeded")
	}
	output := logs.String()
	for _, expected := range []string{"stream=vp8-failure-test", "stage=encode_frame", "status=failed", "Unknown encoder 'libvpx'"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing diagnostic %q: %s", expected, output)
		}
	}
}
func TestStreamDiagnosticValidationAndBoundedStderr(t *testing.T) {
	v, server := streamTestServer(t)
	_ = v
	request, _ := http.NewRequest("POST", server.URL+"/stream/log", strings.NewReader(`{"stream_id":"trace-1","event":"fallback","stage":"video_decode","reason":"decoder failed"}`))
	request.Header.Set("X-Viewer-Token", "test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("diagnostic rejected")
	}
	var stderr encoderStderr
	stderr.Write(bytes.Repeat([]byte("a"), 10000))
	stderr.Write([]byte("last failure"))
	if len(stderr.String()) != 8192 || !strings.HasSuffix(stderr.String(), "last failure") {
		t.Fatal("stderr tail not bounded")
	}
	response = httpPostDiagnostic(t, server, `{"stream_id":"bad\nline","event":"fallback"}`)
	if response.StatusCode != 400 {
		t.Fatal("invalid diagnostic id accepted")
	}
}
func httpPostDiagnostic(t *testing.T, server *httptest.Server, body string) *http.Response {
	t.Helper()
	request, _ := http.NewRequest("POST", server.URL+"/stream/log", strings.NewReader(body))
	request.Header.Set("X-Viewer-Token", "test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response
}

func TestControlHeartbeatKeepsSessionAliveWithoutHTTPPolling(t *testing.T) {
	server := testManager()
	defer server.Shutdown()
	id := bootstrapSession(t, server.Handler())
	session := server.sessions[id]
	session.lastSeen = time.Now().Add(-59 * time.Second)
	if err := session.viewer.controlMessage([]byte(`{"type":"heartbeat"}`), func([]byte) error { return nil }, 0); err != nil {
		t.Fatal(err)
	}
	server.expireSessions(time.Now().Add(2 * time.Second))
	if server.sessions[id] == nil {
		t.Fatal("heartbeat did not keep session alive")
	}
	session.lastSeen = time.Now().Add(-61 * time.Second)
	if err := session.viewer.controlMessage([]byte(`{"type":"heartbeat"}`), func([]byte) error { return nil }, 0); err == nil {
		t.Fatal("heartbeat revived an expired session")
	}
}

func TestControlDisconnectInvalidatesQueuedKeys(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	_, cancel, done, epoch := v.startControl()
	defer cancel()
	if err := v.eventWithEpoch(map[string]interface{}{"type": "key", "vk": float64(65), "down": true}, epoch); err != nil {
		t.Fatal(err)
	}
	queued := <-v.commands
	v.finishControl(done, epoch)
	if queued.controlEpoch == v.controlEpoch.Load() {
		t.Fatal("queued key remains valid after control disconnect")
	}
	if err := v.eventWithEpoch(map[string]interface{}{"type": "key", "vk": float64(65), "down": true}, epoch); err == nil {
		t.Fatal("old control connection can enqueue key events")
	}
}
