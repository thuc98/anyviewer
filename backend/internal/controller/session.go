// Linux control session assembled from the verified registration/relay paths.
// Keeps registration alive while relaying, sends input and returns raw split
// screen updates for a renderer. Go port of control_session.py.
package controller

import (
	"crypto/md5"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"
)

// registrationProfile mirrors the controller-config.json schema.
type registrationProfile struct {
	Terms                           []string `json:"terms"`
	AnsiCodec                       string   `json:"ansi_codec"`
	ExperimentalLinuxMetadataPolicy bool     `json:"experimental_linux_metadata_policy"`
}

// partnerProfile contains credentials supplied for one connection.
type partnerProfile struct {
	RequestTerms []string `json:"request_terms"`
	Password     string   `json:"password"`
	PasswordType int      `json:"password_type"`
}

// Request metadata is protocol framing; target identity and credentials are per session.
func makePartnerProfile(id, password string) partnerProfile {
	return partnerProfile{RequestTerms: []string{"8", id, "", "6.6.133", "", "", "", "", "-1", "0", "0", "0", "", "420", "0", "", ""}, Password: strings.TrimSpace(password), PasswordType: 0}
}

type controlSession struct {
	timeout      time.Duration
	certPolicy   bool
	registration *transport
	relay        *transport
	route        *targetRoute
	codec        string
	settings     viewerSettings

	loginAcknowledged bool
}

func newControlSession(timeout time.Duration, certPolicy bool) *controlSession {
	return &controlSession{timeout: timeout, certPolicy: certPolicy, settings: defaultViewerSettings()}
}

// digits returns n cryptographically-random decimal digits.
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
	stage := "validate profiles"
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", stage, err)
		}
	}()
	s.codec = reg.AnsiCodec
	terms := append([]string(nil), partner.RequestTerms...)
	if len(terms) != 17 {
		return errors.New("partner request needs seventeen terms")
	}
	defer func() {
		if err != nil {
			s.close()
		}
	}()

	stage = "registration tunnel"
	s.registration = newTransport(s.timeout, s.certPolicy)
	if err = s.registration.connectRegistration(defaultRegHost, 443); err != nil {
		return err
	}
	regCmd, err := buildRegistrationCommand(reg.Terms, s.codec)
	if err != nil {
		return err
	}
	stage = "send registration"
	if err = s.registration.sendCommand(regCmd); err != nil {
		return err
	}
	stage = "receive registration identity"
	idPayload, err := s.registration.receiveMode1Payload()
	if err != nil {
		return err
	}
	identity, err := extractCommand3Identity(idPayload, s.codec)
	if err != nil {
		return err
	}

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
	if err = s.registration.sendCommand(connCmd); err != nil {
		return err
	}

	stage = "receive partner route"
	for i := 0; i < 4; i++ {
		response, rerr := s.registration.receiveMode1Payload()
		if rerr != nil {
			return rerr
		}
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

	stage = "relay tunnel"
	s.relay = newTransport(s.timeout, s.certPolicy)
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
	return s.relay.sendRelay(payload, s.route.publicIP, s.route.tcpPort, true)
}

func (s *controlSession) send(payload []byte) error { return s.sendWithEncoding(payload, false) }

func (s *controlSession) sendWithEncoding(payload []byte, unicodeBytes bool) error {
	if s.relay == nil || s.route == nil {
		return errors.New("session is closed")
	}
	return s.relay.sendRelay(payload, s.route.publicIP, s.route.tcpPort, unicodeBytes)
}

// receive reads one relay packet and returns its discriminator kind and fields.
// It transparently answers the login ack (kind 0) and the keepalive (kind 3).
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

// acknowledgeScreen is called by the renderer only after it accepts the update.
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
