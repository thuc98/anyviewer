package controller

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/proxy"
)

const (
	defaultRegHost = "functions3.ultraviewer.net"
	tlsTargetHost  = "ultraviewer"
)

var errTimeout = errors.New("transport deadline")

type transport struct {
	timeout    time.Duration
	maxFrame   int
	certPolicy bool
	conn       net.Conn
	buffer     []byte
	readbuf    []byte
	respHeader []byte
	settings   viewerSettings
	logID      string
	role       string
}

func (t *transport) logf(format string, args ...any) {
	log.Printf("connection=%s transport=%s %s", t.logID, t.role, fmt.Sprintf(format, args...))
}

func newTransport(timeout time.Duration, certPolicy bool) *transport {
	return &transport{
		timeout:    timeout,
		maxFrame:   1024 * 1024,
		certPolicy: certPolicy,
		readbuf:    make([]byte, 4096),
		settings:   defaultViewerSettings(),
		role:       "transport",
	}
}

func (t *transport) tlsConfig() *tls.Config {
	cfg := &tls.Config{ServerName: t.settings.TLSServerName}
	if t.certPolicy {

		cfg.InsecureSkipVerify = true
	}
	return cfg
}

func (t *transport) dial(host string, port int) (conn net.Conn, err error) {
	address := net.JoinHostPort(host, fmt.Sprint(port))
	started := time.Now()
	route := "direct"
	if t.settings.SOCKS5Enabled {
		route = "socks5://" + net.JoinHostPort(t.settings.SOCKS5Host, strconv.Itoa(t.settings.SOCKS5Port))
	}
	t.logf("event=tcp_dial status=starting destination=%q route=%q proxy_auth=%t timeout=%s", address, route, t.settings.SOCKS5Enabled && t.settings.SOCKS5Username != "", t.timeout)
	defer func() {
		if err != nil {
			t.logf("event=tcp_dial status=failed destination=%q route=%q elapsed=%s error=%q", address, route, time.Since(started), err)
		} else {
			t.logf("event=tcp_dial status=connected destination=%q local=%q peer=%q elapsed=%s", address, conn.LocalAddr(), conn.RemoteAddr(), time.Since(started))
		}
	}()
	forward := &net.Dialer{Timeout: t.timeout}
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()
	if !t.settings.SOCKS5Enabled {
		return forward.DialContext(ctx, "tcp", address)
	}
	var auth *proxy.Auth
	if t.settings.SOCKS5Username != "" {
		auth = &proxy.Auth{User: t.settings.SOCKS5Username, Password: t.settings.SOCKS5Password}
	}
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(t.settings.SOCKS5Host, strconv.Itoa(t.settings.SOCKS5Port)), auth, forward)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 proxy: %w", err)
	}
	conn, err = dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 proxy: %w", err)
	}
	return conn, nil
}

func (t *transport) handshake(raw net.Conn) (*tls.Conn, error) {
	started := time.Now()
	t.logf("event=tls_handshake status=starting server_name=%q verify_certificate=%t", t.settings.TLSServerName, !t.certPolicy)
	conn := tls.Client(raw, t.tlsConfig())
	conn.SetDeadline(time.Now().Add(t.timeout))
	if err := conn.Handshake(); err != nil {
		t.logf("event=tls_handshake status=failed elapsed=%s error=%q", time.Since(started), err)
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	state := conn.ConnectionState()
	t.logf("event=tls_handshake status=connected version=%q cipher=%q elapsed=%s", tls.VersionName(state.Version), tls.CipherSuiteName(state.CipherSuite), time.Since(started))
	return conn, nil
}

func (t *transport) connectRegistration(host string, port int) error {
	if t.conn != nil {
		return errors.New("already connected")
	}
	raw, err := t.dial(host, port)
	if err != nil {
		return fmt.Errorf("TCP dial: %w", err)
	}
	conn, err := t.handshake(raw)
	if err != nil {
		raw.Close()
		return fmt.Errorf("TLS handshake: %w", err)
	}
	t.conn = conn
	t.logf("event=http_tunnel status=starting host=%q", host)
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

	firstLine := header
	if i := bytes.Index(header, []byte("\r\n")); i >= 0 {
		firstLine = header[:i]
	}
	t.logf("event=http_tunnel status=response http_status=%q", firstLine)
	if !bytes.Equal(firstLine, []byte("HTTP/1.1 200 OK")) {
		t.Close()
		return errors.New("registration tunnel did not return HTTP 200")
	}
	t.logf("event=http_tunnel status=ready")
	return nil
}

func (t *transport) connectSession(host string, port int, httpHostname string, useSSL bool) error {
	if t.conn != nil {
		return errors.New("already connected")
	}

	raw, err := t.dial(host, port)
	if err != nil {
		return fmt.Errorf("TCP dial: %w", err)
	}
	t.conn = raw
	if useSSL {
		conn, err := t.handshake(raw)
		if err != nil {
			raw.Close()
			t.conn = nil
			return fmt.Errorf("TLS handshake: %w", err)
		}
		t.conn = conn
	}
	if httpHostname != "" {
		t.logf("event=http_tunnel status=starting host=%q", httpHostname)
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
		t.logf("event=http_tunnel status=response http_status=%q", bytes.SplitN(header, []byte("\r\n"), 2)[0])
		if !bytes.HasPrefix(header, []byte("HTTP/1.1 200 ")) {
			t.Close()
			return errors.New("session tunnel did not return HTTP 200")
		}
		t.logf("event=http_tunnel status=ready")
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

func (t *transport) sendCommand(plaintext []byte) error {
	if t.conn == nil {
		return errors.New("not connected")
	}
	encrypted, err := encryptRegistrationBytes(plaintext)
	if err != nil {
		return err
	}

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
		t.logf("event=transport_close status=closed local=%q peer=%q", t.conn.LocalAddr(), t.conn.RemoteAddr())
		t.conn.Close()
		t.conn = nil
	}
	t.buffer = nil
}
