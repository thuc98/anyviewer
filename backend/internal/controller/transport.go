// Recovered registration and relay transports. Windows login is the caller's
// responsibility. Go port of registration_transport.py + session_transport.py.
package controller

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

const (
	defaultRegHost = "functions3.ultraviewer.net"
	tlsTargetHost  = "ultraviewer"
)

// errTimeout mirrors Python's TimeoutError on a receive deadline: the buffer is
// preserved and the caller may retry.
var errTimeout = errors.New("transport deadline")

type transport struct {
	timeout    time.Duration
	maxFrame   int
	certPolicy bool // original-certificate-policy: accept any server certificate
	conn       net.Conn
	buffer     []byte
	readbuf    []byte
	respHeader []byte
}

func newTransport(timeout time.Duration, certPolicy bool) *transport {
	return &transport{
		timeout:    timeout,
		maxFrame:   1024 * 1024,
		certPolicy: certPolicy,
		readbuf:    make([]byte, 4096),
	}
}

func (t *transport) tlsConfig() *tls.Config {
	cfg := &tls.Config{ServerName: tlsTargetHost}
	if t.certPolicy {
		// Original RC40 callback returns true for every certificate.
		cfg.InsecureSkipVerify = true
	}
	return cfg
}

// connectRegistration opens the registration tunnel (HTTP 200 expected).
func (t *transport) connectRegistration(host string, port int) error {
	if t.conn != nil {
		return errors.New("already connected")
	}
	raw, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), t.timeout)
	if err != nil {
		return fmt.Errorf("TCP dial: %w", err)
	}
	conn := tls.Client(raw, t.tlsConfig())
	conn.SetDeadline(time.Now().Add(t.timeout))
	if err := conn.Handshake(); err != nil {
		raw.Close()
		return fmt.Errorf("TLS handshake: %w", err)
	}
	conn.SetDeadline(time.Time{})
	t.conn = conn
	if err := t.writeAll(httpServicePreface(host)); err != nil {
		t.Close()
		return fmt.Errorf("send HTTP preface: %w", err)
	}
	header, err := t.readHTTPHeader()
	if err != nil {
		t.Close()
		return fmt.Errorf("receive HTTP response: %w", err)
	}
	t.respHeader = header
	// Original just strips the header; reconstructed client checks status.
	firstLine := header
	if i := bytes.Index(header, []byte("\r\n")); i >= 0 {
		firstLine = header[:i]
	}
	if !bytes.Equal(firstLine, []byte("HTTP/1.1 200 OK")) {
		t.Close()
		return errors.New("registration tunnel did not return HTTP 200")
	}
	return nil
}

// connectSession opens the relay transport to the resolved endpoint.
func (t *transport) connectSession(host string, port int, httpHostname string, useSSL bool) error {
	if t.conn != nil {
		return errors.New("already connected")
	}

	log.Println("connect to", host)

	raw, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), t.timeout)
	if err != nil {
		return fmt.Errorf("TCP dial: %w", err)
	}
	t.conn = raw
	if useSSL {
		conn := tls.Client(raw, t.tlsConfig())
		conn.SetDeadline(time.Now().Add(t.timeout))
		if err := conn.Handshake(); err != nil {
			raw.Close()
			t.conn = nil
			return fmt.Errorf("TLS handshake: %w", err)
		}
		conn.SetDeadline(time.Time{})
		t.conn = conn
	}
	if httpHostname != "" {
		if err := t.writeAll(httpServicePreface(httpHostname)); err != nil {
			t.Close()
			return fmt.Errorf("send HTTP preface: %w", err)
		}
		header, err := t.readHTTPHeader()
		if err != nil {
			t.Close()
			return fmt.Errorf("receive HTTP response: %w", err)
		}
		t.respHeader = header
		if !bytes.HasPrefix(header, []byte("HTTP/1.1 200 ")) {
			t.Close()
			return errors.New("session tunnel did not return HTTP 200")
		}
	}
	return nil
}

func (t *transport) writeAll(data []byte) error {
	if t.conn == nil {
		return errors.New("not connected")
	}
	t.conn.SetWriteDeadline(time.Now().Add(t.timeout))
	_, err := t.conn.Write(data)
	t.conn.SetWriteDeadline(time.Time{})
	return err
}

// fill performs one bounded read, appending to the buffer. It returns errTimeout
// on the deadline (buffer preserved) and io.EOF when the peer closes.
func (t *transport) fill(deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errTimeout
	}
	t.conn.SetReadDeadline(deadline)
	n, err := t.conn.Read(t.readbuf)
	if n > 0 {
		t.buffer = append(t.buffer, t.readbuf[:n]...)
		return nil
	}
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return errTimeout
		}
		if err == io.EOF {
			return io.EOF
		}
		return err
	}
	return io.EOF
}

func (t *transport) readHTTPHeader() ([]byte, error) {
	if t.conn == nil {
		return nil, errors.New("not connected")
	}
	deadline := time.Now().Add(t.timeout)
	for !bytes.Contains(t.buffer, []byte("\r\n\r\n")) {
		if len(t.buffer) >= 16384 {
			return nil, errors.New("HTTP header exceeds limit")
		}
		if err := t.fill(deadline); err != nil {
			return nil, err
		}
	}
	end := bytes.Index(t.buffer, []byte("\r\n\r\n")) + 4
	header := append([]byte(nil), t.buffer[:end]...)
	t.consume(end)
	return header, nil
}

func (t *transport) consume(n int) {
	rest := make([]byte, len(t.buffer)-n)
	copy(rest, t.buffer[n:])
	t.buffer = rest
}

// sendCommand encrypts a caller-supplied ANSI command and sends a VTCP mode1
// frame with registration flag0.
func (t *transport) sendCommand(plaintext []byte) error {
	if t.conn == nil {
		return errors.New("not connected")
	}
	encrypted, err := encryptRegistrationBytes(plaintext)
	if err != nil {
		return err
	}
	// InitMyPassword passes True as Send argument6; argument5=False becomes
	// byte0 at body offset1 (E151).
	body := append([]byte{0x01, 0x00}, encrypted...)
	if len(body) > t.maxFrame {
		return errors.New("outbound frame exceeds limit")
	}
	frame, err := encodeTransportFrame(body)
	if err != nil {
		return err
	}
	return t.writeAll(frame)
}

// sendRelay sends a relay frame toward the target endpoint.
func (t *transport) sendRelay(payload []byte, targetIP string, targetPort int, unicodeBytes bool) error {
	if t.conn == nil {
		return errors.New("not connected")
	}
	flag := 0
	if unicodeBytes {
		flag = 1
	}
	frame, err := encodeRelayFrame(payload, targetIP, targetPort, flag)
	if err != nil {
		return err
	}
	if len(frame)-4 > t.maxFrame {
		return errors.New("outbound frame exceeds limit")
	}
	return t.writeAll(frame)
}

// receiveBody reads one length-prefixed frame, returning errTimeout on deadline.
func (t *transport) receiveBody() ([]byte, error) {
	if t.conn == nil {
		return nil, errors.New("not connected")
	}
	deadline := time.Now().Add(t.timeout)
	for len(t.buffer) < 4 {
		if err := t.fill(deadline); err != nil {
			return nil, err
		}
	}
	size := int(binary.LittleEndian.Uint32(t.buffer[:4]))
	if size < 1 || size > t.maxFrame {
		return nil, errors.New("invalid inbound frame length")
	}
	for len(t.buffer) < 4+size {
		if err := t.fill(deadline); err != nil {
			return nil, err
		}
	}
	body := append([]byte(nil), t.buffer[4:4+size]...)
	t.consume(4 + size)
	return body, nil
}

func (t *transport) receiveMode1Payload() ([]byte, error) {
	body, err := t.receiveBody()
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || body[0] != 1 {
		return nil, errors.New("expected mode1 frame")
	}
	return body[1:], nil
}

func (t *transport) Close() {
	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
	t.buffer = nil
}
