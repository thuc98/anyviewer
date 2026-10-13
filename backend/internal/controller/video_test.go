package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"image"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

func TestVP8Encoder(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	encoder, err := newVP8Encoder(ctx, 64, 64, 30, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.close()
	pixels := newCanvas(64, 64)
	data, err := encoder.encode(ctx, pixels)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 10 || data[0]&1 != 0 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		t.Fatal("first VP8 sample is not a keyframe")
	}
	data, err = encoder.encode(ctx, pixels)
	if err != nil || len(data) == 0 {
		t.Fatalf("next VP8 sample failed: %v", err)
	}
}

func TestAdaptiveStreamBackpressureAndRecovery(t *testing.T) {
	settings := defaultViewerSettings()
	adaptive := newAdaptiveStream(settings)
	now := time.Now()
	adaptive.observe(settings, now, 200*time.Millisecond, 100000, 0)
	if adaptive.fps >= settings.StreamFPS || adaptive.quality >= settings.StreamQuality {
		t.Fatal("slow delivery did not reduce quality/FPS")
	}
	slowFPS, slowQuality := adaptive.fps, adaptive.quality
	for i := 1; i <= 6; i++ {
		adaptive.observe(settings, now.Add(time.Duration(i)*time.Second), time.Millisecond, 100, 0)
	}
	if adaptive.fps <= slowFPS || adaptive.quality <= slowQuality {
		t.Fatal("healthy delivery did not recover")
	}
	settings.StreamAdaptive = false
	adaptive.observe(settings, now, 5*time.Second, 1000000, 1)
	if adaptive.fps != settings.StreamFPS || adaptive.quality != settings.StreamQuality || adaptive.bitrate != settings.StreamBitrate {
		t.Fatal("manual settings not respected")
	}
}

func TestTrackedDirtyIncludesSkippedUpdatesAndHistoryOverflow(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 64, 64, 32, true)
	pixels := newCanvas(64, 64)
	publishTestScreen(v, pixels, pixels.Rect)
	version := v.screenVersion
	publishTestScreen(v, pixels, image.Rect(2, 3, 4, 5))
	publishTestScreen(v, pixels, image.Rect(8, 9, 10, 11))
	snapshot := v.snapshotScreen(version, false)
	if snapshot.rect != image.Rect(2, 3, 10, 11) || snapshot.full {
		t.Fatalf("lost skipped updates: %+v", snapshot.rect)
	}
	for i := 0; i < 65; i++ {
		publishTestScreen(v, pixels, image.Rect(2, 3, 4, 5))
	}
	if !v.snapshotScreen(version, false).full || len(v.screenChanges) != 64 {
		t.Fatal("history overflow does not trigger bounded full refresh")
	}
}

func TestWebRTCVideoTrackAndResize(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg is not installed")
	}
	v, server := streamTestServer(t)
	v.mu.Lock()
	v.settings.StreamEncoding = "vp8"
	v.settings.StreamTransport = "webrtc"
	v.mu.Unlock()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.CreateDataChannel("screen", nil); err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 8)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Codec().MimeType != webrtc.MimeTypeVP8 {
			return
		}
		var frame []byte
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			var vp8 codecs.VP8Packet
			data, err := vp8.Unmarshal(packet.Payload)
			if err != nil {
				return
			}
			if vp8.S == 1 && vp8.PID == 0 {
				frame = nil
			}
			frame = append(frame, data...)
			if packet.Marker {
				select {
				case frames <- frame:
				default:
				}
				frame = nil
			}
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
		t.Fatal("ICE timeout")
	}
	body, _ := json.Marshal(peer.LocalDescription())
	req, _ := http.NewRequest("POST", server.URL+"/stream/rtc", bytes.NewReader(body))
	req.Header.Set("X-Viewer-Token", "test-token")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var answer webrtc.SessionDescription
	err = json.NewDecoder(response.Body).Decode(&answer)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("video negotiation failed: %v", err)
	}
	if err = peer.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	awaitSize := func(width, height int) {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case data := <-frames:
				if len(data) >= 10 && data[0]&1 == 0 && int(data[6])+(int(data[7]&0x3f)<<8) == width && int(data[8])+(int(data[9]&0x3f)<<8) == height {
					return
				}
			case <-timer.C:
				t.Fatalf("no VP8 keyframe for %dx%d", width, height)
			}
		}
	}
	awaitSize(64, 64)
	resized := newCanvas(128, 96)
	publishTestScreen(v, resized, resized.Rect)
	awaitSize(128, 96)
}
