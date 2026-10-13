// Web portal for the UltraViewer Linux controller.
// Supports dynamic Partner ID & Password from the web interface.
// Go port of server.py (the web-portal variant) + viewer.html.
package controller

import (
	"anyviewer-go/internal/webui"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"image/png"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

type cmdItem struct {
	payload   []byte
	evType    string
	button    string
	vk        int
	down      bool
	unicode   bool
	messageID string
}

type viewerState struct {
	State    string               `json:"state"`
	Frames   int                  `json:"frames"`
	Error    string               `json:"error,omitempty"`
	Messages []chatMessage        `json:"messages,omitempty"`
	Transfer *fileTransferState   `json:"transfer,omitempty"`
	Incoming []*incomingFileState `json:"incoming,omitempty"`
}

// Viewer owns the connection lifecycle and the latest rendered frame.
type Viewer struct {
	reg            registrationProfile
	partner        partnerProfile
	dictionary     []byte
	width          int
	height         int
	blockSize      int
	certPolicy     bool
	timeout        time.Duration
	settings       viewerSettings
	settingsPath   string
	settingsFileMu *sync.Mutex
	onSettings     func(viewerSettings)
	clipboard      clipboardState
	clipboardSent  string

	lifecycle sync.Mutex // serializes connect/disconnect transitions
	closing   bool       // guarded by lifecycle

	mu          sync.Mutex // guards the fields below
	commands    chan cmdItem
	frame       []byte
	state       viewerState
	active      bool
	curStop     chan struct{}
	curDone     chan struct{}
	curStopOnce *sync.Once
	upload      *outgoingFile
	incoming    map[string]*incomingFile
}

func newViewer(reg registrationProfile, dictionary []byte, width, height, blockSize int, certPolicy bool) *Viewer {
	return &Viewer{
		reg:            reg,
		dictionary:     dictionary,
		width:          width,
		height:         height,
		blockSize:      blockSize,
		certPolicy:     certPolicy,
		timeout:        8 * time.Second,
		settings:       defaultViewerSettings(),
		settingsFileMu: &sync.Mutex{},
		commands:       make(chan cmdItem, 1024),
		state:          viewerState{State: "disconnected"},
	}
}

func normalizePartnerID(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\ufeff' {
			return -1
		}
		return r
	}, value)
}

func (v *Viewer) connect(partnerID string, password *string) error {
	partnerID = normalizePartnerID(partnerID)
	if !isDecimal(partnerID) || password == nil || strings.TrimSpace(*password) == "" {
		return errors.New("Partner ID and password are required")
	}
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	if v.closing {
		return errors.New("viewer is shutting down")
	}

	v.mu.Lock()
	active := v.active
	done := v.curDone
	v.mu.Unlock()
	if active {
		v.disconnect()
		if done != nil {
			<-done
		}
	}

	partner := makePartnerProfile(partnerID, *password)

	v.drainCommands()
	stop := make(chan struct{})
	newDone := make(chan struct{})
	once := &sync.Once{}
	v.mu.Lock()
	v.frame = nil
	settings := v.settings
	v.clearUploadLocked()
	v.clearIncomingLocked(false)
	v.state = viewerState{State: "connecting"}
	v.clipboard = clipboardState{}
	v.clipboardSent = ""
	v.active = true
	v.curStop = stop
	v.curDone = newDone
	v.curStopOnce = once
	v.mu.Unlock()

	go v.run(partner, settings, stop, newDone)
	return nil
}

func (v *Viewer) disconnect() {
	v.mu.Lock()
	stop := v.curStop
	once := v.curStopOnce
	v.mu.Unlock()
	if stop != nil && once != nil {
		once.Do(func() { close(stop) })
	}
}

// shutdown waits for key/button releases and transport cleanup before exiting.
func (v *Viewer) shutdown() {
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	v.closing = true
	v.disconnect()
	v.mu.Lock()
	done := v.curDone
	v.mu.Unlock()
	if done != nil {
		<-done
	}
	v.mu.Lock()
	v.clearIncomingLocked(false)
	v.mu.Unlock()
}

func (v *Viewer) drainCommands() {
	for {
		select {
		case <-v.commands:
		default:
			return
		}
	}
}

func stopped(stop chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// event validates an input event, then enqueues it for the relay goroutine.
func (v *Viewer) event(value map[string]interface{}) error {
	evType, _ := value["type"].(string)
	var payload []byte
	var err error
	item := cmdItem{evType: evType}
	switch evType {
	case "move":
		x, e1 := jsonInt(value, "x")
		y, e2 := jsonInt(value, "y")
		if e1 != nil || e2 != nil {
			return errors.New("invalid move event")
		}
		payload, err = mouseMove(x, y)
	case "button":
		b, e1 := jsonStr(value, "button")
		d, e2 := jsonBool(value, "down")
		if e1 != nil || e2 != nil {
			return errors.New("invalid button event")
		}
		payload, err = mouseButton(b, d)
		item.button, item.down = b, d
	case "wheel":
		delta, e := jsonInt(value, "delta")
		if e != nil {
			return errors.New("invalid wheel event")
		}
		payload, err = mouseWheel(delta)
	case "key":
		vk, e1 := jsonInt(value, "vk")
		d, e2 := jsonBool(value, "down")
		if e1 != nil || e2 != nil {
			return errors.New("invalid key event")
		}
		payload, err = keyboard(vk, d, false)
		item.vk, item.down = vk, d
	default:
		return errors.New("unknown input event")
	}
	if err != nil {
		return err
	}
	item.payload = payload

	v.mu.Lock()
	connected := v.state.State == "connected"
	defer v.mu.Unlock()
	if !connected {
		return errors.New("session is not connected")
	}
	select {
	case v.commands <- item:
		return nil
	default:
		return errors.New("input queue is full")
	}
}

func (v *Viewer) setState(state string) {
	v.mu.Lock()
	v.state.State = state
	v.mu.Unlock()
}

func (v *Viewer) run(partner partnerProfile, settings viewerSettings, stop, done chan struct{}) {
	keys := map[int]bool{}
	buttons := map[string]bool{}
	var runErr error
	session := newControlSession(v.timeout, v.certPolicy)
	session.settings = settings

	defer func() {
		for vk := range keys {
			session.key(vk, false)
		}
		for b := range buttons {
			session.button(b, false)
		}
		session.close()
		v.mu.Lock()
		if runErr != nil && !stopped(stop) {
			msg := runErr.Error()
			if msg == "" {
				msg = "error"
			}
			v.state.Error = msg
		}
		v.state.State = "disconnected"
		v.clearIncomingLocked(true)
		v.clipboard = clipboardState{}
		v.clipboardSent = ""
		for i := range v.state.Messages {
			if v.state.Messages[i].Status == "queued" {
				v.state.Messages[i].Status = "failed"
			}
		}
		if v.upload != nil {
			v.state.Transfer.Status = "failed"
			v.state.Transfer.Error = "session disconnected"
			v.clearUploadLocked()
		}
		v.active = false
		v.mu.Unlock()
		close(done)
	}()

	dec, err := newZstdDictDecoder(v.dictionary)
	if err != nil {
		runErr = err
		return
	}
	defer dec.dec.Close()
	screen, err := newRGB555Screen(v.width, v.height, dec, v.blockSize)
	if err != nil {
		runErr = err
		return
	}
	if stopped(stop) {
		return
	}
	if err := session.connect(v.reg, partner); err != nil {
		runErr = err
		return
	}
	session.relay.timeout = 150 * time.Millisecond

	for !stopped(stop) {
	drain:
		for i := 0; i < 128; i++ {
			select {
			case it := <-v.commands:
				if e := session.sendWithEncoding(it.payload, it.unicode); e != nil {
					runErr = e
					return
				}
				if it.messageID != "" {
					v.mu.Lock()
					for i := range v.state.Messages {
						if v.state.Messages[i].ID == it.messageID {
							v.state.Messages[i].Status = "sent"
						}
					}
					v.mu.Unlock()
				}
				switch it.evType {
				case "key":
					if it.down {
						keys[it.vk] = true
					} else {
						delete(keys, it.vk)
					}
				case "button":
					if it.down {
						buttons[it.button] = true
					} else {
						delete(buttons, it.button)
					}
				}
			default:
				break drain
			}
		}

		kind, fields, err := session.receive()
		if err == errTimeout {
			continue
		}
		if err != nil {
			runErr = err
			return
		}
		if kind == 13 || kind == 15 {
			if e := v.handleIncomingFile(session, kind, fields); e != nil {
				v.mu.Lock()
				v.state.Error = e.Error()
				v.mu.Unlock()
			}
			continue
		}
		if kind == 16 {
			v.handleIncomingFile(session, kind, fields)
		}
		if kind == 5 || kind == 33 {
			if e := v.receiveClipboard(kind, fields); e != nil {
				v.mu.Lock()
				v.clipboard.Error = e.Error()
				v.mu.Unlock()
			}
			continue
		}
		if kind == 12 || kind == 14 || kind == 16 || kind == 21 {
			if e := v.handleCommunication(session, kind, fields); e != nil {
				v.mu.Lock()
				if kind != 12 && v.upload != nil {
					v.state.Transfer.Status = "failed"
					v.state.Transfer.Error = e.Error()
					v.clearUploadLocked()
				}
				v.mu.Unlock()
			}
			continue
		}
		switch kind {
		case 0:
			v.setState("connected")
		case 1:
			if _, e := screen.apply(fields); e != nil {
				runErr = e
				return
			}
			var buf bytes.Buffer
			if e := png.Encode(&buf, screen.canvas); e != nil {
				runErr = e
				return
			}
			v.mu.Lock()
			v.frame = buf.Bytes()
			v.state.Frames++
			v.mu.Unlock()
			if e := session.acknowledgeScreen(); e != nil {
				runErr = e
				return
			}
		}
	}
}

// ---- JSON helpers --------------------------------------------------------

func jsonInt(m map[string]interface{}, key string) (int, error) {
	v, ok := m[key]
	if !ok {
		return 0, errors.New("missing field")
	}
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f < -32768 || f > 32767 {
		return 0, errors.New("field is not a supported integer")
	}
	return int(f), nil
}

func jsonStr(m map[string]interface{}, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", errors.New("missing field")
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("field is not a string")
	}
	return s, nil
}

func jsonBool(m map[string]interface{}, key string) (bool, error) {
	v, ok := m[key]
	if !ok {
		return false, errors.New("missing field")
	}
	b, ok := v.(bool)
	if !ok {
		return false, errors.New("field is not a boolean")
	}
	return b, nil
}

// ---- HTTP handler --------------------------------------------------------

func reply(w http.ResponseWriter, code int, data []byte, kind string) {
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	w.Write(data)
}

func (v *Viewer) handler(indexHTML []byte, token string) http.Handler {
	indexHTML = bytes.ReplaceAll(indexHTML, []byte("__TOKEN__"), []byte(token))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		validHost := ok && r.Host == addr.String()
		if ok {
			host, port, err := net.SplitHostPort(addr.String())
			if err == nil && net.ParseIP(host).IsLoopback() {
				validHost = validHost || r.Host == net.JoinHostPort("localhost", port)
			}
		}
		if !validHost {
			reply(w, 403, []byte("{}"), "application/json")
			return
		}
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Viewer-Token")), []byte(token)) != 1 || (origin != "" && origin != "http://"+r.Host) {
				reply(w, 403, []byte("{}"), "application/json")
				return
			}
		}
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html"):
			v.mu.Lock()
			theme := v.settings.Theme
			v.mu.Unlock()
			reply(w, 200, bytes.ReplaceAll(indexHTML, []byte("__THEME__"), []byte(theme)), "text/html; charset=utf-8")
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/assets/"):
			http.FileServer(http.FS(webui.Assets)).ServeHTTP(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/file/download":
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Viewer-Token")), []byte(token)) != 1 {
				reply(w, 403, []byte("{}"), "application/json")
				return
			}
			v.downloadIncomingFile(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/clipboard":
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Viewer-Token")), []byte(token)) != 1 {
				reply(w, 403, []byte("{}"), "application/json")
				return
			}
			v.mu.Lock()
			data, _ := json.Marshal(v.clipboard)
			v.mu.Unlock()
			reply(w, 200, data, "application/json")
		case r.Method == http.MethodGet && r.URL.Path == "/bootstrap":
			v.mu.Lock()
			data, _ := json.Marshal(map[string]interface{}{"token": token, "settings": v.settings})
			v.mu.Unlock()
			reply(w, 200, data, "application/json")
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			v.mu.Lock()
			data, _ := json.Marshal(v.state)
			v.mu.Unlock()
			reply(w, 200, data, "application/json")
		case r.Method == http.MethodGet && r.URL.Path == "/frame.png":
			v.mu.Lock()
			frame := v.frame
			v.mu.Unlock()
			if len(frame) > 0 {
				reply(w, 200, frame, "image/png")
			} else {
				reply(w, 204, nil, "image/png")
			}
		case r.Method == http.MethodPost:
			v.handlePost(w, r)
		default:
			reply(w, 404, []byte("{}"), "application/json")
		}
	})
	return mux
}

func (v *Viewer) handlePost(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/file" {
		v.handleFileUpload(w, r)
		return
	}
	maxPayload := 8192
	if r.URL.Path == "/clipboard" {
		maxPayload = 256 * 1024
	}
	if r.ContentLength > int64(maxPayload) {
		reply(w, 400, []byte(`{"error":"payload too large"}`), "application/json")
		return
	}
	body := make([]byte, 0)
	buf := make([]byte, 8193)
	total := 0
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			total += n
			if total > maxPayload {
				reply(w, 400, []byte(`{"error":"payload too large"}`), "application/json")
				return
			}
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	var value map[string]interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		reply(w, 400, []byte(`{"error":"invalid json"}`), "application/json")
		return
	}

	var err error
	switch r.URL.Path {
	case "/clipboard":
		text, e := jsonStr(value, "text")
		if e != nil {
			err = e
		} else {
			err = v.sendClipboard(text)
		}
	case "/settings":
		var settings viewerSettings
		if e := json.Unmarshal(body, &settings); e != nil {
			err = errors.New("invalid settings")
		} else {
			err = v.updateSettings(settings)
		}
	case "/connect":
		id, _ := value["id"].(string)
		var password *string
		if raw, ok := value["pass"]; ok && raw != nil {
			pass, ok := raw.(string)
			if !ok {
				reply(w, 400, []byte(`{"error":"password must be a string"}`), "application/json")
				return
			}
			password = &pass
		}
		err = v.connect(id, password)
	case "/disconnect":
		v.disconnect()
	case "/chat":
		message, e := jsonStr(value, "message")
		if e != nil {
			err = e
		} else {
			err = v.chat(message)
		}
	case "/file/cancel":
		if id, _ := value["id"].(string); id != "" {
			err = v.cancelIncomingFile(id)
		} else {
			err = v.cancelFile()
		}
	case "/file/accept":
		id, e := jsonStr(value, "id")
		if e != nil {
			err = e
		} else {
			err = v.acceptIncomingFile(id)
		}
	case "/event":
		err = v.event(value)
	default:
		reply(w, 404, []byte("{}"), "application/json")
		return
	}
	if err != nil {
		out, _ := json.Marshal(map[string]string{"error": err.Error()})
		reply(w, 400, out, "application/json")
		return
	}
	reply(w, 200, []byte(`{"ok":true}`), "application/json")
}
