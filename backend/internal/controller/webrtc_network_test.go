package controller

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestDockerAllowedHostRequiresExplicitApproval(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Host = "localhost:18765"
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("172.18.0.2"), Port: 8765}))
	if validHost(request) {
		t.Fatal("Docker host accepted without allowlist")
	}
	t.Setenv("VIEWER_ALLOWED_HOSTS", "localhost:18765,127.0.0.1:18765")
	if !validHost(request) {
		t.Fatal("approved published host rejected")
	}
	request.Host = "attacker.example:18765"
	if validHost(request) {
		t.Fatal("unapproved hostname accepted")
	}
}
func TestDockerWebRTCAdvertisedIPAndPorts(t *testing.T) {
	t.Setenv("VIEWER_WEBRTC_IP", "127.0.0.1")
	t.Setenv("VIEWER_WEBRTC_UDP_MIN", "54000")
	t.Setenv("VIEWER_WEBRTC_UDP_MAX", "54010")
	peer, err := newWebRTCPeer()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.CreateDataChannel("control", nil)
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
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering timed out")
	}
	sdp := peer.LocalDescription().SDP
	if !strings.Contains(sdp, " 127.0.0.1 540") {
		t.Fatal("published host IP or fixed UDP range not advertised")
	}
}
func TestDockerWebRTCRejectsInvalidNetworkSettings(t *testing.T) {
	t.Setenv("VIEWER_WEBRTC_IP", "not-an-ip")
	if _, err := newWebRTCPeer(); err == nil {
		t.Fatal("invalid advertised IP accepted")
	}
	t.Setenv("VIEWER_WEBRTC_IP", "")
	t.Setenv("VIEWER_WEBRTC_UDP_MIN", "50020")
	t.Setenv("VIEWER_WEBRTC_UDP_MAX", "50000")
	if _, err := newWebRTCPeer(); err == nil {
		t.Fatal("invalid UDP range accepted")
	}
}
