package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func decodeTestFrame(t *testing.T, packet []byte) (streamHeader, image.Image) {
	t.Helper()
	if len(packet) < 4 {
		t.Fatal("missing header")
	}
	n := int(binary.BigEndian.Uint32(packet[:4]))
	var header streamHeader
	if err := json.Unmarshal(packet[4:4+n], &header); err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(packet[4+n:]))
	if err != nil {
		t.Fatal(err)
	}
	return header, decoded
}

func publishTestScreen(v *Viewer, pixels *image.RGBA, dirty image.Rectangle) {
	v.screenMu.Lock()
	defer v.screenMu.Unlock()
	v.recordScreenLocked(pixels, dirty)
}
func TestStreamEncodingDirtyAndResize(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 64, 64, 32, true)
	settings := defaultViewerSettings()
	settings.StreamEncoding = "png"
	before := image.NewRGBA(image.Rect(0, 0, 64, 64))
	publishTestScreen(v, before, before.Rect)
	first := v.snapshotScreen(0, false)
	after := cloneCanvas(before)
	after.SetRGBA(10, 20, color.RGBA{R: 255, A: 255})
	publishTestScreen(v, after, image.Rect(10, 20, 11, 21))
	patch := v.snapshotScreen(first.version, false)
	packet, err := encodeStreamFrame(patch, settings)
	if err != nil {
		t.Fatal(err)
	}
	header, decoded := decodeTestFrame(t, packet)
	if header.Full || header.X != 10 || header.Y != 20 || decoded.Bounds().Dx() != 1 || decoded.Bounds().Dy() != 1 {
		t.Fatalf("wrong dirty frame: %+v", header)
	}
	if decoded.At(0, 0) != after.At(10, 20) {
		t.Fatal("wrong patch pixel")
	}
	if v.snapshotScreen(patch.version, false).pixels != nil {
		t.Fatal("unchanged version copied")
	}
	// Subsequent in-place updates cannot change the captured patch.
	after.SetRGBA(10, 20, color.RGBA{B: 255, A: 255})
	if patch.pixels.RGBAAt(0, 0).R != 255 {
		t.Fatal("patch shares mutable screen memory")
	}
	snapshot := v.snapshotScreen(0, false)
	packet, err = encodeStreamFrame(snapshot, settings)
	if err != nil {
		t.Fatal(err)
	}
	header, _ = decodeTestFrame(t, packet)
	if !header.Full || header.Width != 64 {
		t.Fatal("missing reconnect full frame")
	}
	resized := image.NewRGBA(image.Rect(0, 0, 80, 80))
	publishTestScreen(v, resized, resized.Rect)
	snapshot = v.snapshotScreen(patch.version, false)
	if !snapshot.full || snapshot.width != 80 {
		t.Fatal("missing resize full frame")
	}
	settings.StreamEncoding = "jpeg"
	packet, err = encodeStreamFrame(snapshot, settings)
	if err != nil {
		t.Fatal(err)
	}
	header, decoded = decodeTestFrame(t, packet)
	if !header.Full || header.Encoding != "jpeg" || decoded.Bounds().Dx() != 80 {
		t.Fatal("JPEG full-frame option ignored")
	}
}

func TestStreamSettingsValidation(t *testing.T) {
	for _, mutate := range []func(*viewerSettings){
		func(s *viewerSettings) { s.StreamTransport = "invalid" }, func(s *viewerSettings) { s.StreamEncoding = "h264" },
		func(s *viewerSettings) { s.StreamFPS = 61 }, func(s *viewerSettings) { s.StreamQuality = 101 },
	} {
		settings := defaultViewerSettings()
		mutate(&settings)
		if settings.validate() == nil {
			t.Fatal("invalid stream settings accepted")
		}
	}
}

func streamTestServer(t *testing.T) (*Viewer, *httptest.Server) {
	t.Helper()
	v := newViewer(registrationProfile{}, nil, 64, 64, 32, true)
	v.settings.StreamEncoding = "png"
	screen := image.NewRGBA(image.Rect(0, 0, 64, 64))
	publishTestScreen(v, screen, screen.Rect)
	server := httptest.NewServer(v.handler(nil, "test-token"))
	t.Cleanup(func() { v.shutdown(); server.Close() })
	return v, server
}

func TestWebSocketFramesAndAcknowledgement(t *testing.T) {
	v, server := streamTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/stream/ws", &websocket.DialOptions{Subprotocols: []string{"viewer", "test-token", "test-session"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, packet, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	header, _ := decodeTestFrame(t, packet)
	if !header.Full {
		t.Fatal("first frame must be full")
	}
	v.mu.Lock()
	next := cloneCanvas(v.screenImage)
	next.SetRGBA(5, 6, color.RGBA{G: 255, A: 255})
	v.mu.Unlock()
	publishTestScreen(v, next, image.Rect(5, 6, 6, 7))
	// No second frame until browser acknowledges the first.
	arrived := make(chan []byte, 1)
	readErr := make(chan error, 1)
	go func() {
		_, p, e := conn.Read(ctx)
		if e != nil {
			readErr <- e
		} else {
			arrived <- p
		}
	}()
	select {
	case <-arrived:
		t.Fatal("sent without acknowledgement")
	case <-time.After(100 * time.Millisecond):
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("ack")); err != nil {
		t.Fatal(err)
	}
	select {
	case packet = <-arrived:
		header, _ = decodeTestFrame(t, packet)
		if header.Full || header.X != 5 || header.Y != 6 {
			t.Fatalf("wrong patch: %+v", header)
		}
	case err := <-readErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("no patch delivered")
	}
}

func TestWebSocketRejectsBadCredentialsAndOrigin(t *testing.T) {
	_, server := streamTestServer(t)
	for _, options := range []*websocket.DialOptions{
		{Subprotocols: []string{"viewer", "wrong-token", "session"}},
		{Subprotocols: []string{"viewer", "test-token", "session"}, HTTPHeader: http.Header{"Origin": []string{"http://attacker.example"}}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, _, err := websocket.Dial(ctx, server.URL+"/stream/ws", options)
		cancel()
		if err == nil {
			conn.CloseNow()
			t.Fatal("unauthorized websocket accepted")
		}
	}
}

func TestWebRTCFrameDelivery(t *testing.T) {
	v, server := streamTestServer(t)
	// Incompressible pixels force multiple SCTP chunks.
	screen := image.NewRGBA(image.Rect(0, 0, 128, 128))
	rand.New(rand.NewSource(1)).Read(screen.Pix)
	publishTestScreen(v, screen, screen.Rect)
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	channel, err := peer.CreateDataChannel("screen", nil)
	if err != nil {
		t.Fatal(err)
	}
	v.setState("connected")
	control, err := peer.CreateDataChannel("control", nil)
	if err != nil {
		t.Fatal(err)
	}
	controlStatus := make(chan bool, 1)
	controlAcks := make(chan uint64, 2)
	control.OnOpen(func() {
		control.SendText(`{"type":"heartbeat"}`)
		control.SendText(`{"type":"input","id":1,"event":{"type":"key","vk":65,"down":true}}`)
		control.SendText(`{"type":"input","id":2,"event":{"type":"key","vk":65,"down":false}}`)
	})
	control.OnMessage(func(message webrtc.DataChannelMessage) {
		var packet struct {
			Type  string `json:"type"`
			ID    uint64 `json:"id"`
			Error string `json:"error"`
		}
		if json.Unmarshal(message.Data, &packet) != nil {
			return
		}
		if packet.Type == "status" {
			select {
			case controlStatus <- true:
			default:
			}
		}
		if packet.Type == "input_ack" && packet.Error == "" {
			controlAcks <- packet.ID
		}
	})
	frames := make(chan []byte, 1)
	var packet []byte
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		if len(message.Data) == 0 {
			return
		}
		packet = append(packet, message.Data[1:]...)
		if message.Data[0] == 1 {
			frames <- packet
			packet = nil
			channel.SendText("ack")
		}
	})
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(10 * time.Second):
		t.Fatal("client ICE timeout")
	}
	body, _ := json.Marshal(peer.LocalDescription())
	req, _ := http.NewRequest("POST", server.URL+"/stream/rtc", bytes.NewReader(body))
	req.Header.Set("X-Viewer-Token", "test-token")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("negotiation failed: %s", data)
	}
	var answer webrtc.SessionDescription
	if err = json.Unmarshal(data, &answer); err != nil {
		t.Fatal(err)
	}
	if err = peer.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-frames:
		header, decoded := decodeTestFrame(t, received)
		if !header.Full || decoded.Bounds().Dx() != 128 || len(received) <= 16*1024 {
			t.Fatal("invalid RTC frame or chunk reassembly")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no WebRTC frame received")
	}
	select {
	case <-controlStatus:
	case <-time.After(5 * time.Second):
		t.Fatal("WebRTC status not pushed")
	}
	for _, expected := range []uint64{1, 2} {
		select {
		case id := <-controlAcks:
			if id != expected {
				t.Fatal("WebRTC key events reordered")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WebRTC input acknowledgement missing")
		}
	}

}

func TestWebRTCRejectsInvalidOffer(t *testing.T) {
	_, server := streamTestServer(t)
	req, _ := http.NewRequest("POST", server.URL+"/stream/rtc", strings.NewReader(`{"type":"answer"}`))
	req.Header.Set("X-Viewer-Token", "test-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("invalid offer accepted")
	}
}
