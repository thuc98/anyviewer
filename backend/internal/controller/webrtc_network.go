package controller

import (
	"errors"
	"net"
	"os"
	"strconv"

	"github.com/pion/webrtc/v4"
)

func newWebRTCPeer() (*webrtc.PeerConnection, error) {
	var engine webrtc.SettingEngine
	if ip := os.Getenv("VIEWER_WEBRTC_IP"); ip != "" {
		if net.ParseIP(ip) == nil {
			return nil, errors.New("VIEWER_WEBRTC_IP must be an IP address")
		}
		engine.SetNAT1To1IPs([]string{ip}, webrtc.ICECandidateTypeHost)
	}
	low, high := os.Getenv("VIEWER_WEBRTC_UDP_MIN"), os.Getenv("VIEWER_WEBRTC_UDP_MAX")
	if low != "" || high != "" {
		minimum, err1 := strconv.Atoi(low)
		maximum, err2 := strconv.Atoi(high)
		if err1 != nil || err2 != nil || minimum < 1 || maximum > 65535 || minimum > maximum {
			return nil, errors.New("invalid WebRTC UDP port range")
		}
		if err := engine.SetEphemeralUDPPortRange(uint16(minimum), uint16(maximum)); err != nil {
			return nil, err
		}
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(engine)).NewPeerConnection(webrtc.Configuration{})
}
