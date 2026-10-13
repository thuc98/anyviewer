package controller

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPAuthorization(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	handler := v.handler([]byte("const token='__TOKEN__';"), "test-token")
	cases := []struct {
		method, path, host, token, origin string
		status                            int
	}{
		{"GET", "/", "127.0.0.1:8765", "", "", 200},
		{"GET", "/status", "localhost:8765", "", "", 200},
		{"GET", "/frame.png", "127.0.0.1:8765", "", "", 204},
		{"GET", "/status", "attacker.example:8765", "", "", 403},
		{"POST", "/disconnect", "127.0.0.1:8765", "", "", 403},
		{"POST", "/disconnect", "127.0.0.1:8765", "bad", "", 403},
		{"POST", "/disconnect", "127.0.0.1:8765", "test-token", "https://untrusted.example", 403},
		{"POST", "/disconnect", "127.0.0.1:8765", "test-token", "http://127.0.0.1:8765", 200},
		{"POST", "/disconnect", "localhost:8765", "test-token", "http://localhost:8765", 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		req.Host = c.host
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("X-Viewer-Token", c.token)
		req.Header.Set("Origin", c.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Fatalf("%+v: status %d", c, rec.Code)
		}
		if c.path == "/" && !strings.Contains(rec.Body.String(), "const token='test-token';") {
			t.Fatal("token was not injected")
		}
	}
}

func TestShutdownWaitsForWorkerAndRejectsConnect(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.curStop = make(chan struct{})
	v.curDone = make(chan struct{})
	v.curStopOnce = &sync.Once{}
	v.active = true
	finished := make(chan struct{})
	go func() { v.shutdown(); close(finished) }()
	<-v.curStop
	select {
	case <-finished:
		t.Fatal("shutdown exited before cleanup")
	default:
	}
	close(v.curDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	if err := v.connect("123456789", stringPointer("fixture")); err == nil {
		t.Fatal("accepted connection after shutdown")
	}
}

func TestReconnectWaitsForPreviousWorker(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.curStop = make(chan struct{})
	v.curDone = make(chan struct{})
	v.curStopOnce = &sync.Once{}
	v.active = true
	oldStop, oldDone := v.curStop, v.curDone
	result := make(chan error, 1)
	go func() { result <- v.connect("123456789", stringPointer("fixture")) }()
	<-oldStop
	select {
	case <-result:
		t.Fatal("reconnected before cleanup")
	default:
	}
	v.mu.Lock()
	v.active = false
	v.mu.Unlock()
	close(oldDone)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	v.shutdown()
}

func TestInputRejectsFractionalNumbers(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.state.State = "connected"
	for _, value := range []float64{1.5, 32768, 1e20} {
		if err := v.event(map[string]interface{}{"type": "move", "x": value, "y": float64(0)}); err == nil {
			t.Fatalf("accepted x=%v", value)
		}
	}
}

func TestNormalizePartnerID(t *testing.T) {
	for _, value := range []string{" 123 456 789 ", "123\t456\n789", "123\u00a0456\u2003789", "\ufeff123456789"} {
		if got := normalizePartnerID(value); got != "123456789" {
			t.Fatalf("normalized ID = %q", got)
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestDisconnectResponseWaitsUntilRemoteSocketCloses(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.curStop = make(chan struct{})
	v.curDone = make(chan struct{})
	v.curStopOnce = &sync.Once{}
	local, remote := net.Pipe()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := newTransport(30*time.Second, true)
	relay.ctx = ctx
	relay.conn = local
	relay.watchConnection(local)
	go func() {
		<-v.curStop
		cancel()
		relay.Close()
		v.mu.Lock()
		v.state.State = "disconnected"
		v.mu.Unlock()
		close(v.curDone)
	}()
	request := httptest.NewRequest("POST", "/disconnect", strings.NewReader("{}"))
	request.Host = "127.0.0.1:8765"
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
	request.Header.Set("X-Viewer-Token", "fixture-token")
	response := httptest.NewRecorder()
	v.handler(nil, "fixture-token").ServeHTTP(response, request)
	if response.Code != 200 || v.state.State != "disconnected" {
		t.Fatal("disconnect returned before cleanup")
	}
	remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("remote connection still open: %v", err)
	}
}

func TestCancellationInterruptsPendingTransportRead(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := newTransport(30*time.Second, true)
	transport.ctx = ctx
	transport.conn = local
	transport.watchConnection(local)
	done := make(chan error, 1)
	go func() { _, err := transport.receiveBody(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not interrupt blocking read")
	}
	transport.Close()
}
