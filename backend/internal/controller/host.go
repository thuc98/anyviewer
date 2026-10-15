package controller

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type sharingState struct {
	State        string            `json:"state"`
	ID           string            `json:"id,omitempty"`
	Password     string            `json:"password,omitempty"`
	Error        string            `json:"error,omitempty"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Messages     []chatMessage     `json:"messages,omitempty"`
	Frames       uint64            `json:"frames"`
	Capabilities shareCapabilities `json:"capabilities"`
}

type sharingPatch struct {
	version uint64
	rect    image.Rectangle
}

type sharingSession struct {
	sourceID  string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	state     sharingState
	frame     image.Image
	fullJPEG  []byte
	version   uint64
	delivered uint64
	changed   chan struct{}
	patch     []byte
	patchRect image.Rectangle
	history   []sharingPatch
	chatQueue chan chatMessage
	native    bool
	peer      bool
	upload    bool
	reg       registrationProfile
	settings  viewerSettings
	timeout   time.Duration
	workers   sync.WaitGroup
	writeMu   sync.Mutex
}

func (v *Viewer) startSharing(width, height int) error {
	return v.startSharingEncoded(width, height, "jpeg", 92)
}

func (v *Viewer) startSharingEncoded(width, height int, encoding string, quality int) error {
	if encoding != "jpeg" && encoding != "native-jpeg" {
		return errors.New("invalid sharing encoding")
	}
	if quality < 30 || quality > 100 {
		return errors.New("invalid sharing quality")
	}
	if width < 1 || height < 1 || width > 2560 || height > 1440 {
		return errors.New("invalid sharing dimensions")
	}
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	if v.closing {
		return errors.New("viewer is shutting down")
	}
	v.stopSharing()
	reg, err := defaultRegistrationProfile()
	if err != nil {
		return err
	}
	sourceID, err := randomToken()
	if err != nil {
		return err
	}
	reg.Terms[9] = reg.Terms[1]
	reg.Terms[6] = base64.StdEncoding.EncodeToString([]byte("AnyViewer-" + reg.Terms[1][:12]))
	ctx, cancel := context.WithCancel(context.Background())
	v.mu.Lock()
	h := &sharingSession{sourceID: sourceID, ctx: ctx, cancel: cancel, done: make(chan struct{}), reg: reg, settings: v.settings, timeout: v.timeout, state: sharingState{State: "registering", Password: reg.Terms[2], Width: width, Height: height, Capabilities: browserShareCapabilities}}
	h.native = encoding == "native-jpeg"
	h.settings.StreamQuality = quality
	v.sharing = h
	v.mu.Unlock()
	go h.run()
	return nil
}

func (v *Viewer) stopSharing() {
	v.mu.Lock()
	h := v.sharing
	v.mu.Unlock()
	if h != nil {
		h.cancel()
		<-h.done
	}
}
func (h *sharingSession) status() sharingState {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.state
	state.Messages = append([]chatMessage(nil), h.state.Messages...)
	return state
}

func (h *sharingSession) setState(state string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.State = state
	if err != nil {
		h.state.Error = err.Error()
	}
}
func (h *sharingSession) sendRegistration(t *transport, p []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	return t.sendCommand(p)
}
func (h *sharingSession) run() {
	var runErr error
	tr := newTransport(h.timeout, true)
	tr.ctx = h.ctx
	tr.settings = h.settings
	tr.role = "host_registration"
	defer func() {
		h.cancel()
		h.workers.Wait()
		tr.Close()
		h.mu.Lock()
		h.state.State = "stopped"
		h.state.ID = ""
		h.state.Password = ""
		h.frame = nil
		h.fullJPEG = nil
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			h.state.Error = runErr.Error()
		}
		h.mu.Unlock()
		close(h.done)
	}()
	if runErr = tr.connectRegistration(h.settings.RegistrationHost, 443); runErr != nil {
		return
	}
	cmd, err := buildRegistrationCommand(h.reg.Terms, h.reg.AnsiCodec)
	if err != nil {
		runErr = err
		return
	}
	if runErr = h.sendRegistration(tr, cmd); runErr != nil {
		return
	}
	id, err := receiveRegistrationIdentity(tr, h.reg.AnsiCodec)
	if err != nil {
		runErr = err
		return
	}
	h.mu.Lock()
	h.state.ID = strconv.Itoa(id.clientID)
	h.state.State = "ready"
	h.mu.Unlock()
	log.Printf("event=sharing_registration status=ready id=%d", id.clientID)
	tr.timeout = 500 * time.Millisecond
	for h.ctx.Err() == nil {
		payload, err := tr.receiveMode1Payload()
		if err == errTimeout {
			continue
		}
		if err != nil {
			if h.ctx.Err() == nil {
				runErr = err
			}
			return
		}
		fields, err := decodeCommandFields(payload, h.reg.AnsiCodec)
		if err != nil {
			continue
		}
		if len(fields) < 10 || fields[0] != "17" {
			continue
		}
		invitation, err := decodeShareInvitation(fields)
		if err != nil {
			log.Printf("event=sharing_invitation status=rejected error=%q", err)
			continue
		}
		h.mu.Lock()
		busy := h.peer
		if !busy {
			h.peer = true
			h.state.State = "connecting"
		}
		h.mu.Unlock()
		if busy {
			log.Printf("event=sharing_invitation status=busy")
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			err := h.servePeer(tr, invitation)
			h.mu.Lock()
			h.peer = false
			if h.ctx.Err() == nil {
				h.state.State = "ready"
				h.signalLocked()
				if err != nil {
					h.state.Error = err.Error()
				}
			}
			h.mu.Unlock()
			log.Printf("event=sharing_peer status=closed error=%q", err)
		}()
	}
}

type shareInvitation struct {
	requestID, requestHash string
	ips, hosts             []string
}

func decodeShareInvitation(f []string) (shareInvitation, error) {
	var in shareInvitation
	if len(f) < 10 || f[0] != "17" || !isDecimal(f[1]) || len(f[2]) < 1 || len(f[2]) > 128 {
		return in, errors.New("invalid sharing invitation")
	}
	in = shareInvitation{requestID: f[1], requestHash: f[2], ips: strings.Split(f[3], ";"), hosts: strings.Split(f[4], ";")}
	if len(in.ips) == 0 || len(in.ips) > 16 || len(in.ips) != len(in.hosts) {
		return shareInvitation{}, errors.New("invalid sharing relay list")
	}
	for i, ip := range in.ips {
		if net.ParseIP(ip).To4() == nil || !validNetworkHost(in.hosts[i]) {
			return shareInvitation{}, errors.New("invalid sharing relay address")
		}
	}
	return in, nil
}

func (h *sharingSession) servePeer(reg *transport, in shareInvitation) error {
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	var relay *transport
	var relayIP string
	var err error
	for i, ip := range in.ips {
		relay = newTransport(h.timeout, true)
		relay.writeTimeout = h.timeout
		relay.ctx = ctx
		relay.settings = h.settings
		relay.role = "host_relay"
		if err = relay.connectSession(ip, 443, in.hosts[i], true); err == nil {
			relayIP = ip
			break
		}
		relay.Close()
	}
	if err != nil {
		return err
	}
	defer relay.Close()
	hello, _ := encodeTransportFrame([]byte{1, '0'})
	if err = relay.writeAll(hello); err != nil {
		return err
	}
	data, err := relay.receiveMode1Payload()
	if err != nil {
		return err
	}
	f, err := decodeCommandFields(data, h.reg.AnsiCodec)
	if err != nil || len(f) < 3 || f[0] != "0" || !isDecimal(f[1]) || net.ParseIP(f[2]).To4() == nil {
		return errors.New("invalid sharing relay identity")
	}
	response := []string{"17", in.requestID, in.requestHash, relayIP, f[1], f[2], "0", "0|0"}
	if err = h.sendRegistration(reg, []byte(strings.Join(response, ":"))); err != nil {
		return err
	}
	relay.timeout = 15 * time.Second
	body, err := relay.receiveBody()
	if err != nil {
		return err
	}
	packet, err := decodeTransportBody(body)
	if err != nil || packet == nil || packet.kind != "relay" || !packet.flag {
		return errors.New("invalid sharing login packet")
	}
	login, err := decodeUTF16LE(packet.data)
	if err != nil {
		return err
	}
	_, err = verifyShareLogin(login, h.reg.Terms[2], h.reg.AnsiCodec)
	peerIP := net.IPv4(byte(packet.addressValue>>24), byte(packet.addressValue>>16), byte(packet.addressValue>>8), byte(packet.addressValue)).String()
	send := func(p []byte) error { return relay.sendRelay(p, peerIP, int(packet.port), false) }
	if err != nil {
		send(encodeByteArrayFields([][]byte{{2}}))
		return err
	}
	h.mu.Lock()
	width, height := h.state.Width, h.state.Height
	h.state.Error = ""
	h.mu.Unlock()
	if err = send(shareLoginAck(width, height, h.settings.MachineName)); err != nil {
		return err
	}
	h.mu.Lock()
	h.chatQueue = make(chan chatMessage, 32)
	h.state.State = "connected"
	h.delivered = 0
	h.signalLocked()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.chatQueue = nil
		for i := range h.state.Messages {
			if h.state.Messages[i].Status == "queued" {
				h.state.Messages[i].Status = "failed"
			}
		}
	}()
	log.Printf("event=sharing_peer status=authenticated")
	relay.timeout = 5 * time.Millisecond
	var sent uint64
	canSend := false
	var ackDeadline time.Time
	var frameSent time.Time
	statsAt := time.Now()
	var frames, bytesSent, acks int
	var ackTime time.Duration
	for ctx.Err() == nil {
		if elapsed := time.Since(statsAt); elapsed >= 5*time.Second && frames > 0 {
			var average time.Duration
			if acks > 0 {
				average = ackTime / time.Duration(acks)
			}
			log.Printf("event=sharing_stream fps=%.1f kbps=%.0f ack_ms=%.1f", float64(frames)/elapsed.Seconds(), float64(bytesSent)*8/elapsed.Seconds()/1000, float64(average)/float64(time.Millisecond))
			statsAt = time.Now()
			frames = 0
			bytesSent = 0
			acks = 0
			ackTime = 0
		}
		if err := h.sendPendingChat(send); err != nil {
			return err
		}
		if !ackDeadline.IsZero() && time.Now().After(ackDeadline) {
			return errors.New("sharing screen acknowledgement timed out")
		}
		if canSend {
			encoded, version, err := h.nextFrame(sent)
			if err != nil {
				return err
			}
			if encoded != nil {
				if err = send(encoded); err != nil {
					return err
				}
				sent = version
				frames++
				bytesSent += len(encoded)
				frameSent = time.Now()
				canSend = false
				ackDeadline = time.Now().Add(15 * time.Second)
				h.mu.Lock()
				h.state.Frames++
				h.delivered = version
				h.signalLocked()
				h.mu.Unlock()
			}
		}
		body, err = relay.receiveBody()
		if err == errTimeout {
			continue
		}
		if err != nil {
			return err
		}
		p, err := decodeTransportBody(body)
		if err != nil {
			return err
		}
		if p == nil || p.kind != "relay" {
			continue
		}
		if p.addressValue != packet.addressValue || p.port != packet.port {
			continue
		}
		command := p.data
		if p.flag {
			text, err := decodeUTF16LE(command)
			if err != nil {
				return err
			}
			command = []byte(text)
		}
		if p.flag && strings.HasPrefix(string(command), "12|||") {
			h.receiveChat(strings.TrimPrefix(string(command), "12|||"))
			continue
		}
		switch string(command) {
		case "17", "18":
			if string(command) == "18" && !frameSent.IsZero() {
				acks++
				ackTime += time.Since(frameSent)
				frameSent = time.Time{}
			}
			canSend = true
			ackDeadline = time.Time{}
		case "ping":
			if err = send(encodeByteArrayFields([][]byte{{4}})); err != nil {
				return err
			}
		default:
		}
	}
	return ctx.Err()
}

func verifyShareLogin(login, password, codec string) ([]string, error) {
	if !strings.HasPrefix(login, "login:") || len(login) > 8192 {
		return nil, errors.New("invalid sharing login")
	}
	f := strings.Split(strings.TrimPrefix(login, "login:"), ":")
	if len(f) != 22 || !isDecimal(f[9]) {
		return nil, errors.New("invalid sharing login fields")
	}
	expected, err := loginNumericSaltDigest(password, f[0], codec)
	if err != nil || subtle.ConstantTimeCompare([]byte(f[1]), []byte(expected)) != 1 {
		return nil, errors.New("sharing password verification failed")
	}
	secondary, err := connectionRequestPasswordValue(password, 0, codec)
	if err != nil || subtle.ConstantTimeCompare([]byte(f[12]), []byte(secondary)) != 1 {
		return nil, errors.New("sharing password verification failed")
	}
	return f, nil
}
func shareLoginAck(width, height int, name string) []byte {
	u32 := func(n int) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, uint32(n)); return b }
	return encodeByteArrayFields([][]byte{{0}, u32(width), u32(height), u32(width), u32(height), {32}, []byte("6.6.124"), {0}, []byte(name), u32(0), u32(0), {0}, {0, 0}, {0}, {0}, utf16le("")})
}
func encodeShareJPEG(src image.Image, quality int) ([]byte, error) {
	return encodeShareJPEGAt(src, quality, image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
}

func encodeShareJPEGAt(src image.Image, quality int, rect image.Rectangle) ([]byte, error) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 1 || h < 1 || w > 2560 || h > 1440 {
		return nil, errors.New("invalid sharing frame")
	}
	flipped := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		draw.Draw(flipped, image.Rect(0, h-1-y, w, h-y), src, image.Pt(bounds.Min.X, bounds.Min.Y+y), draw.Src)
	}
	for quality = max(30, min(quality, 100)); quality >= 30; quality -= 10 {
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, flipped, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		payload := shareJPEGPacket(encoded.Bytes(), rect)
		if len(payload)+8 <= 1024*1024 {
			return payload, nil
		}
	}
	return nil, errors.New("sharing frame exceeds relay payload limit")
}

func (v *Viewer) sharingStatus() sharingState {
	v.mu.Lock()
	h := v.sharing
	v.mu.Unlock()
	if h == nil {
		return sharingState{State: "stopped", Capabilities: browserShareCapabilities}
	}
	return h.status()
}
func (v *Viewer) serveSharingUpload(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	h := v.sharing
	v.mu.Unlock()
	if h == nil || h.ctx.Err() != nil || r.URL.Query().Get("source_id") != h.sourceID {
		reply(w, 409, []byte(`{"error":"sharing is not active"}`), "application/json")
		return
	}
	h.mu.Lock()
	busy := h.upload
	if !busy {
		h.upload = true
	}
	h.mu.Unlock()
	if busy {
		reply(w, 409, []byte(`{"error":"sharing source already attached"}`), "application/json")
		return
	}
	defer func() { h.mu.Lock(); h.upload = false; h.mu.Unlock(); h.cancel() }()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"viewer"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(2 << 20)
	for h.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		kind, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			return
		}
		if kind == websocket.MessageText && string(data) == "ping" {
		} else if kind == websocket.MessageBinary {
			if err = h.acceptFrame(data); err != nil {
				return
			}
			h.mu.Lock()
			version := h.version
			h.mu.Unlock()
			if err = h.waitDelivery(h.ctx, version); err != nil {
				return
			}
		} else {
			return
		}
		if v.onHeartbeat != nil && !v.onHeartbeat() {
			return
		}
		if err = conn.Write(h.ctx, websocket.MessageText, []byte("ack")); err != nil {
			return
		}
	}
}
func (v *Viewer) serveSharingStatus(w http.ResponseWriter, r *http.Request) {
	data, _ := json.Marshal(v.sharingStatus())
	reply(w, 200, data, "application/json")
}

func shareJPEGPacket(data []byte, rect image.Rectangle) []byte {
	fields := make([][]byte, 11)
	fields[0] = []byte{1}
	fields[7] = data
	fields[8] = make([]byte, 8)
	for i, n := range []int{rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy()} {
		binary.LittleEndian.PutUint16(fields[8][i*2:], uint16(n))
	}
	return encodeByteArrayFields(fields)
}

func (h *sharingSession) acceptFrame(data []byte) error {
	var meta struct {
		Width, Height, X, Y int
		Full                bool
		Encoding            string
	}
	if h.native {
		if len(data) < 4 {
			return errors.New("invalid sharing frame header")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if n < 1 || n > 1024 || n > len(data)-4 {
			return errors.New("invalid sharing frame header")
		}
		if err := json.Unmarshal(data[4:4+n], &meta); err != nil {
			return err
		}
		if meta.Encoding != "native-jpeg" || meta.Width != h.state.Width || meta.Height != h.state.Height {
			return errors.New("invalid sharing frame geometry")
		}
		data = data[4+n:]
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	rect := image.Rect(meta.X, meta.Y, meta.X+cfg.Width, meta.Y+cfg.Height)
	bounds := image.Rect(0, 0, h.state.Width, h.state.Height)
	if !rect.In(bounds) || rect.Empty() || (!h.native && rect != bounds) || (meta.Full && rect != bounds) {
		return errors.New("invalid sharing frame region")
	}
	packet := shareJPEGPacket(data, rect)
	if h.native && len(packet)+8 > 1024*1024 {
		return errors.New("sharing frame exceeds relay payload limit")
	}
	if h.native && !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
		return errors.New("invalid sharing JPEG")
	}
	var img image.Image
	if !h.native || rect != bounds {
		img, err = jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.native {
		if h.version == 0 && rect != bounds {
			return errors.New("sharing requires an initial full frame")
		}
		if rect == bounds {
			h.fullJPEG = data
			h.frame = nil
		} else {
			if h.frame == nil {
				base, err := jpeg.Decode(bytes.NewReader(h.fullJPEG))
				if err != nil {
					return err
				}
				canvas := image.NewRGBA(bounds)
				copySharingPatch(canvas, base, bounds)
				h.frame = canvas
				h.fullJPEG = nil
			}
			copySharingPatch(h.frame.(*image.RGBA), img, rect)
		}
		h.patch = packet
		h.patchRect = rect
	} else {
		h.frame = img
	}
	h.version++
	if h.native {
		h.history = append(h.history, sharingPatch{h.version, rect})
		if len(h.history) > 32 {
			h.history = h.history[1:]
		}
	}
	return nil
}

func (h *sharingSession) nextFrame(sent uint64) ([]byte, uint64, error) {
	h.mu.Lock()
	version := h.version
	if version == 0 || sent == version {
		h.mu.Unlock()
		return nil, version, nil
	}
	if h.native && (sent+1 == version && sent != 0 || h.patchRect == image.Rect(0, 0, h.state.Width, h.state.Height)) {
		packet := h.patch
		h.mu.Unlock()
		return packet, version, nil
	}
	src := h.frame
	rect := src.Bounds()
	if h.native {
		if sent != 0 && len(h.history) > 0 && sent+1 >= h.history[0].version {
			rect = image.Rectangle{}
			for _, patch := range h.history {
				if patch.version > sent {
					rect = rect.Union(patch.rect)
				}
			}
		}
		snapshot := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		draw.Draw(snapshot, snapshot.Bounds(), src, rect.Min, draw.Src)
		src = snapshot
	}
	h.mu.Unlock()
	packet, err := encodeShareJPEGAt(src, h.settings.StreamQuality, rect)
	return packet, version, err
}

func validSharingChat(message string) bool {
	return strings.TrimSpace(message) != "" && !strings.ContainsRune(message, 0) && !strings.Contains(message, "|||") && len(utf16le(message)) <= 4096
}
func (h *sharingSession) appendChatLocked(message chatMessage) {
	h.state.Messages = append(h.state.Messages, message)
	if len(h.state.Messages) > 100 {
		h.state.Messages = h.state.Messages[len(h.state.Messages)-100:]
	}
}
func (h *sharingSession) queueChat(message string) error {
	if !validSharingChat(message) {
		return errors.New("message is empty, too long, or contains an unsupported delimiter")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil || h.state.State != "connected" || h.chatQueue == nil {
		return errors.New("session is not connected")
	}
	item := chatMessage{ID: strconv.FormatInt(time.Now().UnixNano(), 10), Direction: "outgoing", Text: message, Status: "queued"}
	select {
	case h.chatQueue <- item:
		h.appendChatLocked(item)
		return nil
	default:
		return errors.New("input queue is full")
	}
}
func (h *sharingSession) receiveChat(message string) {
	if !validSharingChat(message) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendChatLocked(chatMessage{ID: strconv.FormatInt(time.Now().UnixNano(), 10), Direction: "incoming", Text: message, Status: "received"})
}
func (h *sharingSession) sendPendingChat(send func([]byte) error) error {
	h.mu.Lock()
	queue := h.chatQueue
	h.mu.Unlock()
	for i := 0; i < 4; i++ {
		select {
		case message := <-queue:
			err := send(encodeByteArrayFields([][]byte{{12}, utf16le(message.Text)}))
			h.mu.Lock()
			for i := range h.state.Messages {
				if h.state.Messages[i].ID == message.ID {
					if err == nil {
						h.state.Messages[i].Status = "sent"
					} else {
						h.state.Messages[i].Status = "failed"
					}
					break
				}
			}
			h.mu.Unlock()
			if err != nil {
				return err
			}
		default:
			return nil
		}
	}
	return nil
}

func (h *sharingSession) signalLocked() {
	if h.changed != nil {
		close(h.changed)
	}
	h.changed = make(chan struct{})
}

func (h *sharingSession) waitDelivery(ctx context.Context, version uint64) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	idle := time.NewTimer(200 * time.Millisecond)
	defer idle.Stop()
	for {
		h.mu.Lock()
		connected := h.state.State == "connected"
		delivered := h.delivered >= version
		if h.changed == nil {
			h.changed = make(chan struct{})
		}
		changed := h.changed
		h.mu.Unlock()
		if connected && delivered {
			return nil
		}
		if connected {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-idle.C:
				return nil
			case <-changed:
			}
		}
	}
}

func copySharingPatch(dst *image.RGBA, src image.Image, rect image.Rectangle) {
	for y := 0; y < rect.Dy(); y++ {
		draw.Draw(dst, image.Rect(rect.Min.X, rect.Min.Y+y, rect.Max.X, rect.Min.Y+y+1), src, image.Pt(0, rect.Dy()-1-y), draw.Src)
	}
}
