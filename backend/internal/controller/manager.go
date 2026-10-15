package controller

import (
	"anyviewer-go/internal/webui"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed 8bit.dat
var defaultDictionary []byte

type Options struct {
	ConfigPath               string
	DictionaryPath           string
	Width, Height, BlockSize int
}
type tabSession struct {
	viewer   *Viewer
	handler  http.Handler
	lastSeen time.Time
}
type Server struct {
	base     *Viewer
	token    string
	mu       sync.Mutex
	sessions map[string]*tabSession
	closed   bool
	cleanup  sync.WaitGroup
}

func randomToken() (string, error) {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
func NewServer(options Options) (*Server, error) {
	reg, err := defaultRegistrationProfile()
	if err != nil {
		return nil, err
	}
	data := []byte("{}")
	if options.ConfigPath != "" {
		data, err = os.ReadFile(options.ConfigPath)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &reg); err != nil {
			return nil, err
		}
	}
	dictionary := defaultDictionary
	if options.DictionaryPath != "" {
		dictionary, err = os.ReadFile(options.DictionaryPath)
		if err != nil {
			return nil, err
		}
	}
	base := newViewer(reg, dictionary, options.Width, options.Height, options.BlockSize, true)
	if err := base.loadSettingsData(data); err != nil {
		return nil, err
	}
	if options.ConfigPath != "" {
		base.settingsPath = options.ConfigPath
	} else {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		base.settingsPath = filepath.Join(dir, "AnyViewer", "settings.json")
		base.standaloneSettings = true
		if _, err := os.Stat(base.settingsPath); err == nil {
			if err := base.loadSettings(base.settingsPath); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	return &Server{base: base, token: token, sessions: make(map[string]*tabSession)}, nil
}
func validHost(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	if r.Host == addr.String() {
		return true
	}
	for _, host := range strings.Split(os.Getenv("VIEWER_ALLOWED_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" && r.Host == host {
			return true
		}
	}
	host, port, err := net.SplitHostPort(addr.String())
	return err == nil && net.ParseIP(host).IsLoopback() && r.Host == net.JoinHostPort("localhost", port)
}
func (s *Server) createSession() (string, *tabSession, error) {
	id, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", nil, errors.New("server is shutting down")
	}
	s.base.mu.Lock()
	viewer := newViewer(s.base.reg, s.base.dictionary, s.base.width, s.base.height, s.base.blockSize, s.base.certPolicy)
	viewer.settings = s.base.settings
	viewer.settingsPath = s.base.settingsPath
	viewer.standaloneSettings = s.base.standaloneSettings
	viewer.settingsFileMu = s.base.settingsFileMu
	s.base.mu.Unlock()
	viewer.onSettings = func(settings viewerSettings) { s.base.mu.Lock(); s.base.settings = settings; s.base.mu.Unlock() }
	session := &tabSession{viewer: viewer, handler: viewer.handler(webui.Index, s.token), lastSeen: time.Now()}
	s.sessions[id] = session
	viewer.onHeartbeat = func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.sessions[id] != session || time.Since(session.lastSeen) >= time.Minute {
			return false
		}
		session.lastSeen = time.Now()
		return true
	}
	return id, session, nil
}
func (s *Server) Handler() http.Handler {
	public := s.base.handler(webui.Index, s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validHost(r) {
			reply(w, 403, []byte("{}"), "application/json")
			return
		}
		if r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/index.html" || len(r.URL.Path) >= 8 && r.URL.Path[:8] == "/assets/") {
			public.ServeHTTP(w, r)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/bootstrap" {
			s.expireSessions(time.Now())
			id := r.Header.Get("X-Viewer-Session")
			var session *tabSession
			if id != "" {
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Viewer-Token")), []byte(s.token)) != 1 {
					reply(w, 403, []byte("{}"), "application/json")
					return
				}
				s.mu.Lock()
				session = s.sessions[id]
				if session != nil {
					session.lastSeen = time.Now()
				}
				s.mu.Unlock()
			}
			resumed := session != nil
			if session == nil {
				var err error
				id, session, err = s.createSession()
				if err != nil {
					reply(w, 503, []byte(`{"error":"cannot create session"}`), "application/json")
					return
				}
			}
			session.viewer.mu.Lock()
			settings := session.viewer.settings
			session.viewer.mu.Unlock()
			data, _ := json.Marshal(map[string]interface{}{"token": s.token, "session": id, "settings": settings, "resumed": resumed})
			reply(w, 200, data, "application/json")
			return
		}
		if r.Method == "GET" && (r.URL.Path == "/stream/ws" || r.URL.Path == "/stream/control" || r.URL.Path == "/share/ws") {
			credentials := streamCredentials(r)
			r.Header.Set("X-Viewer-Token", credentials[0])
			r.Header.Set("X-Viewer-Session", credentials[1])
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Viewer-Token")), []byte(s.token)) != 1 {
			reply(w, 403, []byte("{}"), "application/json")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.Host {
			reply(w, 403, []byte("{}"), "application/json")
			return
		}
		s.expireSessions(time.Now())
		id := r.Header.Get("X-Viewer-Session")
		s.mu.Lock()
		session := s.sessions[id]
		if session != nil {
			session.lastSeen = time.Now()
		}
		s.mu.Unlock()
		if session == nil {
			reply(w, 410, []byte(`{"error":"session expired; reload this tab"}`), "application/json")
			return
		}
		if r.Method == "POST" && r.URL.Path == "/session/close" {
			s.mu.Lock()
			delete(s.sessions, id)
			s.mu.Unlock()
			session.viewer.shutdown()
			reply(w, 200, []byte(`{"ok":true}`), "application/json")
			return
		}
		session.handler.ServeHTTP(w, r)
	})
}
func (s *Server) expireSessions(now time.Time) {
	var expired []*Viewer
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for id, session := range s.sessions {
		if now.Sub(session.lastSeen) >= time.Minute {
			delete(s.sessions, id)
			expired = append(expired, session.viewer)
			s.cleanup.Add(1)
		}
	}
	s.mu.Unlock()
	for _, viewer := range expired {
		go func(v *Viewer) { defer s.cleanup.Done(); v.shutdown() }(viewer)
	}
}
func (s *Server) Shutdown() {
	s.mu.Lock()
	s.closed = true
	var viewers []*Viewer
	for _, session := range s.sessions {
		viewers = append(viewers, session.viewer)
	}
	s.sessions = make(map[string]*tabSession)
	s.mu.Unlock()
	var done sync.WaitGroup
	for _, viewer := range viewers {
		viewer.disconnect()
		done.Add(1)
		go func(v *Viewer) { defer done.Done(); v.shutdown() }(viewer)
	}
	done.Wait()
	s.cleanup.Wait()
}
func (s *Server) Serve(ln net.Listener) error {
	server := &http.Server{Handler: s.Handler()}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				s.expireSessions(now)
			case <-stop:
				return
			}
		}
	}()
	err := server.Serve(ln)
	close(stop)
	s.Shutdown()
	if closeErr := server.Shutdown(context.Background()); closeErr != nil {
		return closeErr
	}
	return err
}
