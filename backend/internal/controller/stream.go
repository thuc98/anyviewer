package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image/jpeg"
	"image/png"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/pion/rtcp"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func streamCredentials(r *http.Request) [2]string {
	parts := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) != "viewer" {
		return [2]string{}
	}
	return [2]string{strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])}
}

func (v *Viewer) startStream() (context.Context, context.CancelFunc) {
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	if v.closing {
		cancel()
		return ctx, cancel
	}
	v.mu.Lock()
	if v.streamCancel != nil {
		v.streamCancel()
	}
	v.streamCancel = cancel
	v.mu.Unlock()
	return ctx, cancel
}

type streamHeader struct {
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Full     bool   `json:"full"`
	Encoding string `json:"encoding"`
}

func encodeStreamFrame(snapshot streamSnapshot, settings viewerSettings) ([]byte, error) {
	if snapshot.pixels == nil {
		return nil, nil
	}
	encoding := settings.StreamEncoding
	if encoding == "vp8" {
		encoding = "jpeg"
	}
	header, err := json.Marshal(streamHeader{snapshot.width, snapshot.height, snapshot.rect.Min.X, snapshot.rect.Min.Y, snapshot.full, encoding})
	if err != nil {
		return nil, err
	}
	var packet bytes.Buffer
	if err = binary.Write(&packet, binary.BigEndian, uint32(len(header))); err != nil {
		return nil, err
	}
	packet.Write(header)
	if encoding == "jpeg" {
		err = jpeg.Encode(&packet, snapshot.pixels, &jpeg.Options{Quality: settings.StreamQuality})
	} else {
		err = png.Encode(&packet, snapshot.pixels)
	}
	return packet.Bytes(), err
}

func (v *Viewer) pumpStream(ctx context.Context, transport string, send func([]byte) error, ack <-chan struct{}) error {
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	var version uint64
	var lastSettings viewerSettings
	var next, keyframe time.Time
	var adaptive adaptiveStream
	var meter streamMeter
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if now.Before(next) {
				continue
			}
			v.mu.Lock()
			settings := v.settings
			v.mu.Unlock()
			changed := !sameStreamSettings(settings, lastSettings)
			if changed {
				adaptive = newAdaptiveStream(settings)
			}
			snapshot := v.snapshotScreen(version, changed || !settings.StreamDirty || now.Sub(keyframe) >= 5*time.Second)
			if snapshot.pixels == nil {
				continue
			}
			if snapshot.full {
				keyframe = now
			}
			effective := settings
			effective.StreamQuality = adaptive.quality
			started := time.Now()
			packet, err := encodeStreamFrame(snapshot, effective)
			if err != nil {
				return err
			}
			encoded := time.Since(started)
			if err = send(packet); err != nil {
				return err
			}
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				return errors.New("stream render acknowledgement timed out")
			case <-ack:
				timer.Stop()
			}
			elapsed := time.Since(started)
			adaptive.observe(settings, time.Now(), elapsed, len(packet), 0)
			skipped := uint64(0)
			if version != 0 && snapshot.version > version+1 {
				skipped = snapshot.version - version - 1
			}
			encoding := settings.StreamEncoding
			if encoding == "vp8" {
				encoding = "jpeg"
			}
			v.measureStream(&meter, transport, encoding, adaptive, len(packet), elapsed, encoded, skipped)
			version, lastSettings = snapshot.version, settings
			next = started.Add(time.Second / time.Duration(adaptive.fps))
		}
	}
}

func (v *Viewer) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	id := streamID(r)
	v.mu.Lock()
	requested := v.settings.StreamEncoding
	v.mu.Unlock()
	encoding := requested
	if encoding == "vp8" {
		encoding = "jpeg"
	}
	streamLogf(id, "event=stream_websocket status=starting requested_encoding=%q encoding=%q", requested, encoding)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"viewer"}})
	if err != nil {
		streamLogf(id, "event=stream_websocket status=failed stage=accept error=%q", err)
		return
	}
	streamLogf(id, "event=stream_websocket status=connected encoding=%q", encoding)
	defer conn.CloseNow()
	conn.SetReadLimit(64)
	ctx, cancel := v.startStream()
	defer cancel()
	ack := make(chan struct{}, 1)
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText && string(data) == "ack" {
				select {
				case ack <- struct{}{}:
				default:
				}
			}
		}
	}()
	err = v.pumpStream(ctx, "websocket", func(packet []byte) error {
		timeout, done := context.WithTimeout(ctx, 10*time.Second)
		defer done()
		return conn.Write(timeout, websocket.MessageBinary, packet)
	}, ack)
	streamLogf(id, "event=stream_websocket status=stopped error=%q", err)
}

func (v *Viewer) serveWebRTC(w http.ResponseWriter, r *http.Request) {
	id := streamID(r)
	started := time.Now()
	fail := func(stage string, err error) {
		streamLogf(id, "event=stream_webrtc status=failed stage=%s elapsed=%s error=%q", stage, time.Since(started), err)
	}
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&offer); err != nil || offer.Type != webrtc.SDPTypeOffer {
		fail("decode_offer", errors.New("invalid WebRTC offer"))
		reply(w, 400, []byte(`{"error":"invalid WebRTC offer"}`), "application/json")
		return
	}
	v.mu.Lock()
	encoding := v.settings.StreamEncoding
	video := encoding == "vp8"
	v.mu.Unlock()
	streamLogf(id, "event=stream_webrtc status=starting encoding=%q", encoding)
	if video {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			fail("find_ffmpeg", err)
			reply(w, 503, []byte(`{"error":"VP8 video requires FFmpeg with libvpx on the backend"}`), "application/json")
			return
		}
	}
	peer, err := newWebRTCPeer()
	if err != nil {
		fail("create_peer", err)
		reply(w, 500, []byte(`{"error":"cannot create WebRTC peer"}`), "application/json")
		return
	}
	ctx, cancel := v.startStream()
	go func() { <-ctx.Done(); peer.Close() }()
	var track *webrtc.TrackLocalStaticSample
	feedback := make(chan videoFeedback, 1)
	keyframes := make(chan struct{}, 1)
	if video {
		track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "screen", "ultraview")
		if err != nil {
			cancel()
			fail("create_track", err)
			reply(w, 500, []byte(`{"error":"cannot create video track"}`), "application/json")
			return
		}
		sender, err := peer.AddTrack(track)
		if err != nil {
			cancel()
			fail("add_track", err)
			reply(w, 500, []byte(`{"error":"cannot add video track"}`), "application/json")
			return
		}
		go func() {
			for {
				packets, _, err := sender.ReadRTCP()
				if err != nil {
					return
				}
				for _, packet := range packets {
					switch packet.(type) {
					case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
						select {
						case keyframes <- struct{}{}:
						default:
						}
					}
				}
			}
		}()
	}
	opened := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-opened:
		case <-timer.C:
			fail("data_channel_open", errors.New("data channel did not open within 20 seconds"))
			cancel()
		case <-ctx.Done():
		}
	}()
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) { streamLogf(id, "event=stream_ice status=%s", state.String()) })
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		streamLogf(id, "event=stream_peer status=%s", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateDisconnected {
			cancel()
		}
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		streamLogf(id, "event=stream_data_channel status=received label=%q ordered=%t", channel.Label(), channel.Ordered())
		if !channel.Ordered() || channel.MaxRetransmits() != nil || channel.MaxPacketLifeTime() != nil {
			cancel()
			return
		}
		if channel.Label() == "control" {
			v.attachControlChannel(ctx, channel, id)
			return
		}
		if channel.Label() != "screen" {
			cancel()
			return
		}
		ack := make(chan struct{}, 1)
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if video && message.IsString && len(message.Data) < 1024 {
				var value videoFeedback
				if json.Unmarshal(message.Data, &value) == nil && value.Type == "feedback" && value.RTT >= 0 && value.RTT <= 10000 && value.Loss >= 0 && value.Loss <= 1 && value.Jitter >= 0 && value.Jitter <= 10000 {
					select {
					case feedback <- value:
					default:
					}
				}
				return
			}
			if message.IsString && string(message.Data) == "ack" {
				select {
				case ack <- struct{}{}:
				default:
				}
			}
		})
		channel.OnOpen(func() {
			streamLogf(id, "event=stream_data_channel status=open encoding=%q", encoding)
			select {
			case opened <- struct{}{}:
			default:
			}
			go func() {
				defer cancel()
				if video {
					v.pumpVideo(ctx, track, feedback, keyframes, id)
					return
				}
				err := v.pumpStream(ctx, "webrtc", func(packet []byte) error {
					deadline := time.NewTimer(10 * time.Second)
					defer deadline.Stop()
					pacing := time.NewTicker(5 * time.Millisecond)
					defer pacing.Stop()
					for len(packet) > 0 {
						for channel.BufferedAmount() > 256<<10 {
							select {
							case <-ctx.Done():
								return ctx.Err()
							case <-deadline.C:
								return errors.New("WebRTC send timed out")
							case <-pacing.C:
							}
						}
						n := len(packet)
						if n > 16*1024-1 {
							n = 16*1024 - 1
						}
						chunk := make([]byte, n+1)
						if n == len(packet) {
							chunk[0] = 1
						}
						copy(chunk[1:], packet[:n])
						if err := channel.Send(chunk); err != nil {
							return err
						}
						packet = packet[n:]
					}
					return nil
				}, ack)
				streamLogf(id, "event=stream_data_channel status=stopped error=%q", err)
			}()
		})
	})
	if err = peer.SetRemoteDescription(offer); err != nil {
		cancel()
		fail("set_remote_description", err)
		reply(w, 400, []byte(`{"error":"invalid WebRTC SDP"}`), "application/json")
		return
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		cancel()
		fail("create_answer", err)
		reply(w, 500, []byte(`{"error":"cannot create WebRTC answer"}`), "application/json")
		return
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(answer); err != nil {
		cancel()
		fail("set_local_description", err)
		reply(w, 500, []byte(`{"error":"cannot set WebRTC answer"}`), "application/json")
		return
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-gathered:
		streamLogf(id, "event=stream_webrtc status=answer_ready elapsed=%s", time.Since(started))
		data, _ := json.Marshal(peer.LocalDescription())
		reply(w, 200, data, "application/json")
	case <-r.Context().Done():
		fail("http_request", r.Context().Err())
		cancel()
	case <-ctx.Done():
		reply(w, 503, []byte(`{"error":"WebRTC negotiation canceled"}`), "application/json")
	case <-timer.C:
		fail("ice_gathering", errors.New("ICE gathering timed out"))
		cancel()
		reply(w, 504, []byte(`{"error":"WebRTC ICE gathering timed out"}`), "application/json")
	}
}
