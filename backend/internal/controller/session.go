package controller

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type registrationProfile struct {
	Terms                           []string `json:"terms"`
	AnsiCodec                       string   `json:"ansi_codec"`
	ExperimentalLinuxMetadataPolicy bool     `json:"experimental_linux_metadata_policy"`
}

func defaultRegistrationProfile() (registrationProfile, error) {
	identity, err := randomToken()
	if err != nil {
		return registrationProfile{}, err
	}
	return registrationProfile{
		AnsiCodec:                       "cp1252",
		ExperimentalLinuxMetadataPolicy: true,
		Terms: []string{"3", identity[:32], digits(8), "0", "AnyViewer", "0",
			"aG9zdGluZw==", "6.6.133", "QW55Vmlld2Vy", "", "0|6|0|8|0|0",
			"0", "", "", "0", "en", "user", "0", "0", digits(9), "0", "1", "0:0:0", "", "", ""},
	}, nil
}

type partnerProfile struct {
	RequestTerms []string `json:"request_terms"`
	Password     string   `json:"password"`
	PasswordType int      `json:"password_type"`
}

func makePartnerProfile(id, password string) partnerProfile {
	return partnerProfile{RequestTerms: []string{"8", id, "", "6.6.133", "", "", "", "", "-1", "0", "0", "0", "", "420", "0", "", ""}, Password: strings.TrimSpace(password), PasswordType: 0}
}

type controlSession struct {
	logID        string
	timeout      time.Duration
	certPolicy   bool
	registration *transport
	relay        *transport
	route        *targetRoute
	codec        string
	settings     viewerSettings

	loginAcknowledged bool
}

var connectionSequence atomic.Uint64

const partnerRouteTimeout = 30 * time.Second

func firstField(payload []byte) string {
	if i := bytes.IndexByte(payload, ':'); i >= 0 && i < 8 {
		return string(payload[:i])
	}
	if len(payload) > 8 {
		payload = payload[:8]
	}
	return fmt.Sprintf("%x", payload)
}

func (s *controlSession) logf(format string, args ...any) {
	log.Printf("connection=%s %s", s.logID, fmt.Sprintf(format, args...))
}

func newControlSession(timeout time.Duration, certPolicy bool) *controlSession {
	return &controlSession{logID: strconv.FormatUint(connectionSequence.Add(1), 10), timeout: timeout, certPolicy: certPolicy, settings: defaultViewerSettings()}
}

func digits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String()
}

func (s *controlSession) connect(reg registrationProfile, partner partnerProfile) (err error) {
	s.close()
	started := time.Now()
	stage := "validate profiles"
	s.logf("event=connect status=starting")
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", stage, err)
			s.logf("event=connect status=failed stage=%q elapsed=%s error=%q", stage, time.Since(started), err)
		}
	}()
	s.codec = reg.AnsiCodec
	terms := append([]string(nil), partner.RequestTerms...)
	if len(terms) != 17 {
		return errors.New("partner request needs seventeen terms")
	}
	s.logf("event=connect status=validated partner_id=%q", terms[1])
	regCmd, err := buildRegistrationCommand(reg.Terms, s.codec)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.close()
		}
	}()

	stage = "registration tunnel"
	s.registration = newTransport(s.timeout, s.certPolicy)
	s.registration.logID, s.registration.role = s.logID, "registration"
	s.registration.settings = s.settings
	if err = s.registration.connectRegistration(s.settings.RegistrationHost, 443); err != nil {
		return err
	}
	stage = "send registration"
	s.logf("event=registration status=sending")
	if err = s.registration.sendCommand(regCmd); err != nil {
		return err
	}
	stage = "receive registration identity"
	s.logf("event=registration status=waiting_for_identity")
	idPayload, err := s.registration.receiveMode1Payload()
	if err != nil {
		return err
	}
	identity, err := extractCommand3Identity(idPayload, s.codec)
	if err != nil {
		return err
	}

	s.logf("event=registration status=identity_received")
	terms[2], err = connectionRequestPasswordValue(partner.Password, partner.PasswordType, s.codec)
	if err != nil {
		return err
	}
	terms[6] = strconv.Itoa(identity.clientID)
	hashInput, err := ansiEncode(terms[6]+reg.Terms[2], s.codec)
	if err != nil {
		return err
	}
	terms[7] = fmt.Sprintf("%x", md5.Sum(hashInput))

	connCmd, err := buildConnectionRequestCommand(terms, s.codec)
	if err != nil {
		return err
	}
	stage = "send partner request"
	s.logf("event=partner_request status=sending partner_id=%q", terms[1])
	if err = s.registration.sendCommand(connCmd); err != nil {
		return err
	}

	stage = "receive partner route"
	s.logf("event=partner_route status=waiting timeout=%s", partnerRouteTimeout)
	s.registration.timeout = partnerRouteTimeout
	defer func(d time.Duration) { s.registration.timeout = d }(s.timeout)
	for i := 0; i < 4; i++ {
		response, rerr := s.registration.receiveMode1Payload()
		if rerr != nil {
			return rerr
		}
		s.logf("event=partner_route status=frame index=%d prefix=%q", i, firstField(response))
		if strings.HasPrefix(string(response), "8:") {
			text, derr := ansiDecode(response, s.codec)
			if derr != nil {
				return derr
			}
			parts := strings.Split(text, ":")
			if len(parts) >= 2 && parts[1] != "1" {
				switch parts[1] {
				case "2":
					return errors.New("Sai mật khẩu (Wrong Password)")
				case "11":
					return errors.New("Đối tác không online hoặc ID không tồn tại")
				case "99":
					return errors.New("Không thể kết nối tới đối tác (Status 99)")
				default:
					return fmt.Errorf("Lỗi kết nối từ máy chủ (Mã: %s)", parts[1])
				}
			}
			route, rderr := decodeTargetRoute(response, s.codec)
			if rderr != nil {
				return rderr
			}
			s.route = &route
			break
		}
	}
	if s.route == nil {
		return errors.New("Không nhận được thông tin định tuyến từ đối tác")
	}
	s.logf("event=partner_route status=received relay=%q http_host=%q target=%q lan=%q command_port=%d", net.JoinHostPort(s.route.tcpServerIP, "443"), s.route.tcpServerHostname, net.JoinHostPort(s.route.publicIP, strconv.Itoa(s.route.tcpPort)), net.JoinHostPort(s.route.lanIP, strconv.Itoa(s.route.lanPort)), s.route.commandPort)

	stage = "relay tunnel"
	s.relay = newTransport(s.timeout, s.certPolicy)
	s.relay.logID, s.relay.role = s.logID, "relay"
	s.relay.settings = s.settings
	if err = s.relay.connectSession(s.route.tcpServerIP, 443, s.route.tcpServerHostname, true); err != nil {
		return err
	}

	hostname := s.settings.MachineName
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	fields := []string{
		"", "", "1280", "720", strconv.Itoa(s.settings.JPEGQuality), "15", "15", "0", "0", strconv.Itoa(identity.clientID),
		hostname, "0", "", "", digits(16), "2", "80", "0", "0",
		s.route.partnerSecretToken, "6.6.133", "0",
	}
	payload, err := buildLoginWithPassword(fields, partner.Password, partner.PasswordType, digits(8), s.codec)
	if err != nil {
		return err
	}
	stage = "send partner login"
	s.logf("event=partner_login status=sending")
	if err = s.relay.sendRelay(payload, s.route.publicIP, s.route.tcpPort, true); err != nil {
		return err
	}
	s.logf("event=partner_login status=waiting_for_ack elapsed=%s", time.Since(started))
	return nil
}

func (s *controlSession) send(payload []byte) error { return s.sendWithEncoding(payload, false) }

func (s *controlSession) sendWithEncoding(payload []byte, unicodeBytes bool) error {
	if s.relay == nil || s.route == nil {
		return errors.New("session is closed")
	}
	return s.relay.sendRelay(payload, s.route.publicIP, s.route.tcpPort, unicodeBytes)
}

func (s *controlSession) receive() (int, [][]byte, error) {
	if s.relay == nil {
		return 0, nil, errors.New("session is closed")
	}
	body, err := s.relay.receiveBody()
	if err != nil {
		return 0, nil, err
	}
	packet, err := decodeTransportBody(body)
	if err != nil {
		return 0, nil, err
	}
	if packet == nil || packet.kind != "relay" {
		return 0, nil, errors.New("unexpected session transport packet")
	}
	fields, err := decodeByteArrayFields(packet.data)
	if err != nil {
		return 0, nil, err
	}
	if len(fields) == 0 || len(fields[0]) == 0 {
		return 0, nil, errors.New("empty session packet")
	}
	kind := int(fields[0][0])
	if kind == 0 {
		s.loginAcknowledged = true
		if err := s.send([]byte("17")); err != nil {
			return kind, fields, err
		}
	} else if kind == 3 && len(fields) > 1 && len(fields[1]) == 4 {
		value := i32(fields[1])
		if err := s.send([]byte("|||4" + strconv.Itoa(int(value)))); err != nil {
			return kind, fields, err
		}
	}
	return kind, fields, nil
}

func (s *controlSession) acknowledgeScreen() error {
	return s.send([]byte("18"))
}

func (s *controlSession) key(vk int, down bool) error {
	p, err := keyboard(vk, down, false)
	if err != nil {
		return err
	}
	return s.send(p)
}

func (s *controlSession) button(button string, down bool) error {
	p, err := mouseButton(button, down)
	if err != nil {
		return err
	}
	return s.send(p)
}

func (s *controlSession) close() {
	if s.relay != nil {
		s.relay.Close()
		s.relay = nil
	}
	if s.registration != nil {
		s.registration.Close()
		s.registration = nil
	}
	s.route = nil
	s.loginAcknowledged = false
}
