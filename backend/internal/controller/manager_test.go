package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func managerRequest(handler http.Handler, method, path, session, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "127.0.0.1:8765"
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
	request.Header.Set("X-Viewer-Token", "fixture-token")
	request.Header.Set("X-Viewer-Session", session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func testManager() *Server {
	return &Server{base: newViewer(registrationProfile{}, nil, 1, 1, 32, true), token: "fixture-token", sessions: make(map[string]*tabSession)}
}
func bootstrapSession(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := managerRequest(handler, "GET", "/bootstrap", "", "")
	var data struct {
		Session string `json:"session"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &data) != nil || data.Session == "" {
		t.Fatal("bootstrap failed")
	}
	return data.Session
}
func TestTabsHaveIndependentStateAndCommands(t *testing.T) {
	server := testManager()
	defer server.Shutdown()
	handler := server.Handler()
	a, b := bootstrapSession(t, handler), bootstrapSession(t, handler)
	if a == b {
		t.Fatal("tabs share a session")
	}
	first, second := server.sessions[a].viewer, server.sessions[b].viewer
	first.state = viewerState{State: "connected", Frames: 10}
	first.frame = []byte("frame A")
	second.state = viewerState{State: "connected", Frames: 20}
	second.frame = []byte("frame B")
	first.curStop = make(chan struct{})
	first.curStopOnce = &sync.Once{}
	second.curStop = make(chan struct{})
	second.curStopOnce = &sync.Once{}
	if response := managerRequest(handler, "GET", "/frame.png", a, ""); response.Body.String() != "frame A" {
		t.Fatal("wrong tab frame")
	}
	if response := managerRequest(handler, "GET", "/frame.png", b, ""); response.Body.String() != "frame B" {
		t.Fatal("wrong tab frame")
	}
	response := managerRequest(handler, "POST", "/chat", a, `{"message":"only A"}`)
	if response.Code != 200 || len(first.state.Messages) != 1 || len(second.state.Messages) != 0 || len(second.commands) != 0 {
		t.Fatal("chat crossed sessions")
	}
	managerRequest(handler, "POST", "/disconnect", a, `{}`)
	if !stopped(first.curStop) || stopped(second.curStop) {
		t.Fatal("disconnect affected another tab")
	}
	managerRequest(handler, "POST", "/session/close", a, `{}`)
	if response := managerRequest(handler, "GET", "/status", a, ""); response.Code != 410 {
		t.Fatal("closed session remained accessible")
	}
	if response := managerRequest(handler, "GET", "/status", b, ""); response.Code != 200 {
		t.Fatal("closing A affected B")
	}
}
func TestNoFallbackPartnerAndExpiredSessions(t *testing.T) {
	server := testManager()
	defer server.Shutdown()
	handler := server.Handler()
	id := bootstrapSession(t, handler)
	for _, body := range []string{`{}`, `{"id":"123456789"}`, `{"id":"123456789","pass":""}`} {
		if response := managerRequest(handler, "POST", "/connect", id, body); response.Code != 400 {
			t.Fatal("connection accepted without explicit credentials")
		}
	}
	if server.sessions[id].viewer.active {
		t.Fatal("invalid request started a connection")
	}
	if response := managerRequest(handler, "GET", "/status", "unknown", ""); response.Code != 410 {
		t.Fatal("unknown session accessible")
	}
	server.sessions[id].lastSeen = time.Now().Add(-6 * time.Minute)
	server.expireSessions(time.Now())
	if response := managerRequest(handler, "GET", "/status", id, ""); response.Code != 410 {
		t.Fatal("expired tab session retained")
	}
}
func TestSettingsDoNotChangeAnotherTab(t *testing.T) {
	server := testManager()
	defer server.Shutdown()
	handler := server.Handler()
	a, b := bootstrapSession(t, handler), bootstrapSession(t, handler)
	first, second := server.sessions[a].viewer, server.sessions[b].viewer
	first.state.State = "connected"
	second.state.State = "connected"
	settings := first.settings
	settings.JPEGQuality = 70
	if err := first.updateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if second.settings.JPEGQuality == 70 || len(second.commands) != 0 {
		t.Fatal("quality setting affected another tab")
	}
	c := bootstrapSession(t, handler)
	if server.sessions[c].viewer.settings.JPEGQuality != 70 {
		t.Fatal("new tab didn't inherit saved defaults")
	}
}

func TestDefaultRegistrationWithoutConfig(t *testing.T) {
	first, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	second, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildRegistrationCommand(first.Terms, first.AnsiCodec); err != nil {
		t.Fatal(err)
	}
	if first.Terms[1] == second.Terms[1] {
		t.Fatal("generated controllers share an identity")
	}
	if len(first.Terms[1]) != 32 || len(first.Terms[2]) != 8 || !isDecimal(first.Terms[2]) {
		t.Fatal("invalid generated registration credentials")
	}
}

func TestRegistrationRejectsInvalidMetadata(t *testing.T) {
	profile, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"QW55Vmlld2Vy", "0|6|0|8|0", "0|6|0|8|0|name"} {
		profile.Terms[10] = value
		if _, err := buildRegistrationCommand(profile.Terms, profile.AnsiCodec); err == nil {
			t.Fatal("invalid registration metadata accepted")
		}
	}
}
