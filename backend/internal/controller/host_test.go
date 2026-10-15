package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSharingAuthenticationAndGeometry(t *testing.T) {
	fields := make([]string, 22)
	fields[9] = "12345"
	login, err := buildLoginWithPassword(fields, "12345678", 0, "87654321", "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	text, err := decodeUTF16LE(login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyShareLogin(text, "12345678", "cp1252"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{text, strings.Replace(text, "login:", "other:", 1), "login:x"} {
		if _, err := verifyShareLogin(invalid, "wrong-password", "cp1252"); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	ack, err := decodeByteArrayFields(shareLoginAck(1280, 720, "host"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ack) != 16 || ack[0][0] != 0 {
		t.Fatal("invalid login acknowledgement")
	}
	for index, want := range map[int]int{1: 1280, 2: 720, 3: 1280, 4: 720} {
		if len(ack[index]) != 4 || int(binary.LittleEndian.Uint32(ack[index])) != want {
			t.Fatal("invalid screen geometry")
		}
	}
}

func TestRegistrationIdentityAfterNotice(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	tr := newTransport(time.Second, true)
	tr.conn = client
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		for _, payload := range []string{"52:1", "3:1234:5678:192.0.2.1"} {
			frame, _ := encodeTransportFrame(append([]byte{1}, []byte(payload)...))
			if _, err := server.Write(frame); err != nil {
				return
			}
		}
	}()
	id, err := receiveRegistrationIdentity(tr, "cp1252")
	if err != nil {
		t.Fatal(err)
	}
	if id.clientID != 5678 || tr.timeout != time.Second {
		t.Fatal("registration notice changed identity or timeout")
	}
	<-done
}

func sharingTestImage() *image.RGBA {
	src := image.NewRGBA(image.Rect(0, 0, 65, 49))
	for y := 0; y < 49; y++ {
		for x := 0; x < 65; x++ {
			pixel := color.RGBA{R: 240, A: 255}
			if y >= 24 {
				pixel = color.RGBA{G: 240, A: 255}
			}
			src.SetRGBA(x, y, pixel)
		}
	}
	return src
}
func TestSharingJPEGRoundTrip(t *testing.T) {
	payload, err := encodeShareJPEG(sharingTestImage(), 75)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := decodeByteArrayFields(payload)
	if err != nil {
		t.Fatal(err)
	}
	screen, err := newRGB555Screen(65, 49, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas.RGBAAt(0, 0).R < 220 || screen.canvas.RGBAAt(64, 48).G < 220 {
		t.Fatal("JPEG atlas is flipped or tile layout is wrong")
	}
}

func TestSharingInvitationValidation(t *testing.T) {
	f := []string{"17", "12345", "request-hash", "192.0.2.1;192.0.2.2", "relay1.example;relay2.example", "-1", "-1", "0", "0", ""}
	if _, err := decodeShareInvitation(f); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 2, 3, 4} {
		invalid := append([]string(nil), f...)
		invalid[index] = ""
		if _, err := decodeShareInvitation(invalid); err == nil {
			t.Fatalf("accepted malformed field %d", index)
		}
	}
}

func TestSharingUploadAuthenticationAndCleanup(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 65, 49, 32, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &sharingSession{sourceID: "current-source", ctx: ctx, cancel: cancel, state: sharingState{State: "ready", Width: 65, Height: 49}}
	v.sharing = h
	server := httptest.NewServer(v.handler([]byte("index"), "fixture-token"))
	defer server.Close()
	response, err := http.Get(server.URL + "/share")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("credentials exposed without token")
	}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/share/ws?source_id=current-source"
	if conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"viewer", "wrong-token", "session"}}); err == nil {
		conn.CloseNow()
		t.Fatal("unauthorized sharing upload accepted")
	}
	stale := strings.Replace(url, "current-source", "stale-source", 1)
	if conn, _, err := websocket.Dial(ctx, stale, &websocket.DialOptions{Subprotocols: []string{"viewer", "fixture-token", "session"}}); err == nil {
		conn.CloseNow()
		t.Fatal("stale source attached to current session")
	}
	if ctx.Err() != nil {
		t.Fatal("stale upload stopped current session")
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"viewer", "fixture-token", "session"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var frame bytes.Buffer
	if err = jpeg.Encode(&frame, sharingTestImage(), nil); err != nil {
		t.Fatal(err)
	}
	if err = conn.Write(ctx, websocket.MessageBinary, frame.Bytes()); err != nil {
		t.Fatal(err)
	}
	_, ack, err := conn.Read(ctx)
	if err != nil || string(ack) != "ack" {
		t.Fatal("frame was not acknowledged", err)
	}
	h.mu.Lock()
	received := h.version == 1 && h.frame.Bounds() == image.Rect(0, 0, 65, 49)
	h.mu.Unlock()
	if !received {
		t.Fatal("sharing upload did not update latest frame")
	}
	conn.CloseNow()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lost source did not stop sharing")
	}
}

func TestSharingLiveRelay(t *testing.T) {
	if os.Getenv("UV_SHARING_INTEGRATION") != "1" {
		t.Skip("set UV_SHARING_INTEGRATION=1 to exercise the live relay")
	}
	addresses, err := net.LookupIP(defaultRegHost)
	if err != nil {
		t.Fatal(err)
	}
	var selected net.IP
	for _, address := range addresses {
		if ip := address.To4(); ip != nil {
			selected = ip
			break
		}
	}
	if selected == nil {
		t.Fatal("registration service has no IPv4 address")
	}
	v := newViewer(registrationProfile{}, defaultDictionary, 65, 49, 32, true)
	v.settings.RegistrationHost = selected.String()
	defer v.shutdown()
	if err := v.startSharingEncoded(65, 49, "native-jpeg", 92); err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	h := v.sharing
	v.mu.Unlock()
	data, _ := nativeSharingUpload(t, sharingTestImage(), image.Rect(0, 0, 65, 49))
	if err := h.acceptFrame(data); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for h.status().State == "registering" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	state := h.status()
	if state.State != "ready" {
		t.Fatalf("host registration failed: %s", state.Error)
	}
	client := newControlSession(8*time.Second, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client.ctx = ctx
	client.settings.RegistrationHost = selected.String()
	defer client.close()
	reg, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	reg.Terms[9] = reg.Terms[1]
	if err = client.connect(reg, makePartnerProfile(state.ID, state.Password)); err != nil {
		t.Fatal(err)
	}
	kind, _, err := client.receive()
	if err != nil || kind != 0 {
		t.Fatalf("host login failed: kind=%d error=%v", kind, err)
	}
	kind, fields, err := client.receive()
	if err != nil || kind != 1 {
		t.Fatalf("host frame failed: kind=%d error=%v", kind, err)
	}
	screen, err := newRGB555Screen(65, 49, nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas.RGBAAt(0, 0).R < 220 || screen.canvas.RGBAAt(64, 48).G < 220 {
		t.Fatal("live shared image differs")
	}
	patch := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			patch.SetRGBA(x, y, color.RGBA{B: 240, A: 255})
		}
	}
	data, _ = nativeSharingUpload(t, patch, image.Rect(32, 16, 40, 24))
	if err = h.acceptFrame(data); err != nil {
		t.Fatal(err)
	}
	delivery := make(chan error, 1)
	go func() { delivery <- h.waitDelivery(ctx, 2) }()
	select {
	case <-delivery:
		t.Fatal("slow peer did not hold upload acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	if err = client.acknowledgeScreen(); err != nil {
		t.Fatal(err)
	}

	kind, fields, err = client.receive()
	if err != nil || kind != 1 {
		t.Fatalf("live dirty frame failed: kind=%d error=%v", kind, err)
	}
	select {
	case err := <-delivery:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not release queued frame")
	}

	if _, err = screen.apply(fields); err != nil {
		t.Fatal(err)
	}
	if screen.canvas.RGBAAt(34, 18).B < 220 || screen.canvas.RGBAAt(0, 0).R < 220 {
		t.Fatal("live dirty image differs")
	}
	if err = client.acknowledgeScreen(); err != nil {
		t.Fatal(err)
	}
	if err = client.sendWithEncoding(utf16le("12|||Xin chào từ đối tác 👋"), true); err != nil {
		t.Fatal(err)
	}
	chatDeadline := time.Now().Add(3 * time.Second)
	for len(h.status().Messages) == 0 && time.Now().Before(chatDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	messages := h.status().Messages
	if len(messages) != 1 || messages[0].Text != "Xin chào từ đối tác 👋" {
		t.Fatal("native-to-browser chat was lost")
	}
	if err = h.queueChat("Chào từ trình duyệt 👋"); err != nil {
		t.Fatal(err)
	}
	kind, fields, err = client.receive()
	if err != nil || kind != 12 || len(fields) < 2 {
		t.Fatalf("browser-to-native chat failed: kind=%d error=%v", kind, err)
	}
	if text, _ := decodeUTF16LE(fields[1]); text != "Chào từ trình duyệt 👋" {
		t.Fatal("chat Unicode was corrupted")
	}
	if h.status().Capabilities != browserShareCapabilities {
		t.Fatal("browser source advertised control capabilities")
	}
	wrong := newControlSession(8*time.Second, true)
	wrong.ctx = ctx
	wrong.settings.RegistrationHost = selected.String()
	defer wrong.close()
	wrongReg, _ := defaultRegistrationProfile()
	wrongReg.Terms[9] = wrongReg.Terms[1]
	if err = wrong.connect(wrongReg, makePartnerProfile(state.ID, "incorrect-password")); err == nil || !strings.Contains(err.Error(), "Wrong Password") {
		t.Fatal("registration did not reject incorrect password")
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("host did not stop")
	}
	if h.status().ID != "" || h.status().Password != "" {
		t.Fatal("stopped sharing exposed usable credentials")
	}
	t.Log("Live registration, password authentication, relay login, JPEG frame and stop passed")
}

func nativeSharingUpload(t *testing.T, src image.Image, rect image.Rectangle) ([]byte, []byte) {
	t.Helper()
	packet, err := encodeShareJPEGAt(src, 98, rect)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := decodeByteArrayFields(packet)
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(map[string]interface{}{"width": 65, "height": 49, "x": rect.Min.X, "y": rect.Min.Y, "encoding": "native-jpeg", "full": rect == image.Rect(0, 0, 65, 49)})
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, uint32(len(header)))
	data = append(data, header...)
	data = append(data, fields[7]...)
	return data, fields[7]
}

func TestSharingNativePatchesAndCatchup(t *testing.T) {
	h := &sharingSession{native: true, state: sharingState{Width: 65, Height: 49}, settings: viewerSettings{StreamQuality: 98}}
	full, _ := nativeSharingUpload(t, sharingTestImage(), image.Rect(0, 0, 65, 49))
	if err := h.acceptFrame(full); err != nil {
		t.Fatal(err)
	}
	screen, _ := newRGB555Screen(65, 49, nil, 32)
	apply := func(sent uint64) uint64 {
		t.Helper()
		p, v, err := h.nextFrame(sent)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := decodeByteArrayFields(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = screen.apply(fields); err != nil {
			t.Fatal(err)
		}
		return v
	}
	sent := apply(0)
	patch := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			patch.SetRGBA(x, y, color.RGBA{B: 240, A: 255})
		}
	}
	upload, jpegBytes := nativeSharingUpload(t, patch, image.Rect(32, 16, 40, 24))
	if err := h.acceptFrame(upload); err != nil {
		t.Fatal(err)
	}
	p, _, err := h.nextFrame(sent)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := decodeByteArrayFields(p)
	if !bytes.Equal(fields[7], jpegBytes) {
		t.Fatal("native JPEG was recompressed")
	}
	sent = apply(sent)
	if screen.canvas.RGBAAt(34, 18).B < 220 || screen.canvas.RGBAAt(0, 0).R < 220 {
		t.Fatal("dirty patch changed the wrong region")
	}
	for _, rect := range []image.Rectangle{image.Rect(0, 32, 8, 40), image.Rect(48, 32, 56, 40)} {
		data, _ := nativeSharingUpload(t, patch, rect)
		if err := h.acceptFrame(data); err != nil {
			t.Fatal(err)
		}
	}
	apply(sent)
	if screen.canvas.RGBAAt(2, 34).B < 220 || screen.canvas.RGBAAt(50, 34).B < 220 {
		t.Fatal("catch-up lost a skipped dirty region")
	}
	screen, _ = newRGB555Screen(65, 49, nil, 32)
	apply(0)
	if screen.canvas.RGBAAt(34, 18).B < 220 || screen.canvas.RGBAAt(50, 34).B < 220 || screen.canvas.RGBAAt(64, 48).G < 220 {
		t.Fatal("new viewer received an incomplete image")
	}
}

func TestSharingRejectsInvalidNativeFrames(t *testing.T) {
	h := &sharingSession{native: true, state: sharingState{Width: 65, Height: 49}}
	partial, _ := nativeSharingUpload(t, image.NewRGBA(image.Rect(0, 0, 8, 8)), image.Rect(1, 1, 9, 9))
	outside, _ := nativeSharingUpload(t, image.NewRGBA(image.Rect(0, 0, 8, 8)), image.Rect(64, 48, 72, 56))
	for _, data := range [][]byte{{}, {0, 0, 9, 0}, partial, outside} {
		if err := h.acceptFrame(data); err == nil {
			t.Fatal("invalid or incomplete frame accepted")
		}
	}
	if h.version != 0 || h.frame != nil {
		t.Fatal("rejected frame changed the screen")
	}
}

func TestSharingChatWireAndState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &sharingSession{ctx: ctx, state: sharingState{State: "ready"}}
	if err := h.queueChat("hello"); err == nil {
		t.Fatal("chat accepted without an authenticated peer")
	}
	h.state.State = "connected"
	h.chatQueue = make(chan chatMessage, 32)
	for _, invalid := range []string{"", "bad|||delimiter", strings.Repeat("a", 2049)} {
		if err := h.queueChat(invalid); err == nil {
			t.Fatal("invalid chat accepted")
		}
	}
	const text = "Xin chào 👋"
	if err := h.queueChat(text); err != nil {
		t.Fatal(err)
	}
	before := h.status()
	if before.Messages[0].Status != "queued" {
		t.Fatal("message not queued")
	}
	if err := h.sendPendingChat(func(packet []byte) error {
		fields, err := decodeByteArrayFields(packet)
		if err != nil {
			return err
		}
		if len(fields) != 2 || fields[0][0] != 12 || !bytes.Equal(fields[1], utf16le(text)) {
			t.Fatal("wrong host chat wire format")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if h.status().Messages[0].Status != "sent" || before.Messages[0].Status != "queued" {
		t.Fatal("message status or snapshot isolation is wrong")
	}
	h.receiveChat("Đã nhận 👋")
	if got := h.status().Messages[1]; got.Direction != "incoming" || got.Text != "Đã nhận 👋" {
		t.Fatal("incoming chat was lost")
	}
	if err := h.queueChat("fail"); err != nil {
		t.Fatal(err)
	}
	if err := h.sendPendingChat(func([]byte) error { return context.Canceled }); err == nil {
		t.Fatal("send failure ignored")
	}
	if h.status().Messages[2].Status != "failed" {
		t.Fatal("failed message marked successful")
	}
	cancel()
	if err := h.queueChat("closed"); err == nil {
		t.Fatal("chat accepted after sharing ended")
	}
}

func TestSharingChatHTTPIsolation(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	h := &sharingSession{sourceID: "current-source", ctx: context.Background(), state: sharingState{State: "connected"}, chatQueue: make(chan chatMessage, 32)}
	v.sharing = h
	handler := v.handler(nil, "fixture-token")
	request := func(source, token string) int {
		body, _ := json.Marshal(map[string]string{"source_id": source, "message": "Xin chào"})
		req := httptest.NewRequest(http.MethodPost, "/share/chat", bytes.NewReader(body))
		req.Host = "127.0.0.1:8765"
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("X-Viewer-Token", token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if request("current-source", "invalid") != 403 {
		t.Fatal("unauthenticated chat accepted")
	}
	if request("old-source", "fixture-token") != 400 {
		t.Fatal("stale source chat accepted")
	}
	if len(h.chatQueue) != 0 {
		t.Fatal("rejected chat was queued")
	}
	if request("current-source", "fixture-token") != 200 || len(h.chatQueue) != 1 {
		t.Fatal("active sharing chat was not queued")
	}
	if len(v.state.Messages) != 0 {
		t.Fatal("sharing chat leaked into outgoing viewer session")
	}
}

func TestSharingUploadWaitsForRelayDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &sharingSession{ctx: ctx, state: sharingState{State: "connected"}}
	delivered := make(chan error, 1)
	go func() { delivered <- h.waitDelivery(ctx, 2) }()
	select {
	case <-delivered:
		t.Fatal("upload acknowledged before relay delivery")
	case <-time.After(25 * time.Millisecond):
	}
	h.mu.Lock()
	h.delivered = 1
	h.signalLocked()
	h.mu.Unlock()
	select {
	case <-delivered:
		t.Fatal("previous frame released next upload")
	case <-time.After(25 * time.Millisecond):
	}
	h.mu.Lock()
	h.delivered = 2
	h.signalLocked()
	h.mu.Unlock()
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay delivery did not release upload")
	}
	go func() { delivered <- h.waitDelivery(ctx, 3) }()
	cancel()
	select {
	case err := <-delivered:
		if err == nil {
			t.Fatal("cancelled delivery accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled upload remained blocked")
	}
}

func TestSharingFullJPEGStaysCompressedUntilPatch(t *testing.T) {
	h := &sharingSession{native: true, state: sharingState{Width: 65, Height: 49}}
	data, jpegBytes := nativeSharingUpload(t, sharingTestImage(), image.Rect(0, 0, 65, 49))
	if err := h.acceptFrame(data); err != nil {
		t.Fatal(err)
	}
	if h.frame != nil || !bytes.Equal(h.fullJPEG, jpegBytes) {
		t.Fatal("full JPEG was unnecessarily decoded")
	}
	packet, _, err := h.nextFrame(0)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := decodeByteArrayFields(packet)
	if !bytes.Equal(fields[7], jpegBytes) || h.frame != nil {
		t.Fatal("full JPEG passthrough changed")
	}
	patch, _ := nativeSharingUpload(t, image.NewRGBA(image.Rect(0, 0, 8, 8)), image.Rect(0, 0, 8, 8))
	if err := h.acceptFrame(patch); err != nil {
		t.Fatal(err)
	}
	if h.frame == nil || h.fullJPEG != nil {
		t.Fatal("partial patch did not materialize its base")
	}
	if h.frame.At(64, 48).(color.RGBA).G < 220 {
		t.Fatal("lazy reconstruction lost the full frame")
	}
	if err := h.acceptFrame(data[:len(data)-2]); err == nil {
		t.Fatal("truncated native JPEG accepted")
	}
}

func TestSharingLiveFullHD(t *testing.T) {
	if os.Getenv("UV_SHARING_INTEGRATION") != "1" {
		t.Skip("set UV_SHARING_INTEGRATION=1 to exercise the live relay")
	}
	addresses, err := net.LookupIP(defaultRegHost)
	if err != nil {
		t.Fatal(err)
	}
	var host string
	for _, address := range addresses {
		if ip := address.To4(); ip != nil {
			host = ip.String()
			break
		}
	}
	if host == "" {
		t.Fatal("registration service has no IPv4 address")
	}
	v := newViewer(registrationProfile{}, defaultDictionary, 1920, 1080, 32, true)
	v.settings.RegistrationHost = host
	defer v.shutdown()
	if err = v.startSharingEncoded(1920, 1080, "native-jpeg", 92); err != nil {
		t.Fatal(err)
	}
	h := v.sharing
	data := benchmarkSharingFrame(t)
	if err = h.acceptFrame(data); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for h.status().State == "registering" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	state := h.status()
	if state.State != "ready" {
		t.Fatalf("registration failed: %s", state.Error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := newControlSession(8*time.Second, true)
	client.ctx = ctx
	client.settings.RegistrationHost = host
	defer client.close()
	reg, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	reg.Terms[9] = reg.Terms[1]
	if err = client.connect(reg, makePartnerProfile(state.ID, state.Password)); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := client.receive(); err != nil || kind != 0 {
		t.Fatalf("login failed: kind=%d error=%v", kind, err)
	}
	const count = 30
	production := make(chan error, 1)
	go func() {
		for i := 1; i < count; i++ {
			started := time.Now()
			if err := h.waitDelivery(ctx, uint64(i)); err != nil {
				production <- err
				return
			}
			select {
			case <-ctx.Done():
				production <- ctx.Err()
				return
			case <-time.After(max(0, time.Second/30-time.Since(started))):
			}
			if err := h.acceptFrame(data); err != nil {
				production <- err
				return
			}
		}
		production <- nil
	}()
	started := time.Now()
	var bytesReceived int
	for i := 0; i < count; i++ {
		kind, fields, err := client.receive()
		if err != nil || kind != 1 {
			t.Fatalf("frame %d failed: kind=%d error=%v", i, kind, err)
		}
		image, err := jpeg.Decode(bytes.NewReader(fields[7]))
		if err != nil {
			t.Fatal(err)
		}
		if image.Bounds().Dx() != 1920 || image.Bounds().Dy() != 1080 {
			t.Fatal("Full HD dimensions changed")
		}
		bytesReceived += len(fields[7])
		if err = client.acknowledgeScreen(); err != nil {
			t.Fatal(err)
		}
	}
	if err = <-production; err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	t.Logf("Full HD real-relay delivery: frames=%d fps=%.1f jpeg_kbps=%.0f duration=%s", count, float64(count)/elapsed.Seconds(), float64(bytesReceived)*8/elapsed.Seconds()/1000, elapsed)
}
