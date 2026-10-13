package controller

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func serveSOCKS5TLS(conn net.Conn, certificate tls.Certificate, authenticated bool, expectedHost, expectedSNI string) error {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	method := byte(0)
	if authenticated {
		method = 2
	}
	if header[0] != 5 || !bytes.Contains(methods, []byte{method}) {
		return fmt.Errorf("unexpected SOCKS5 greeting")
	}
	if _, err := conn.Write([]byte{5, method}); err != nil {
		return err
	}
	if authenticated {
		if _, err := io.ReadFull(conn, header); err != nil {
			return err
		}
		user := make([]byte, int(header[1]))
		if _, err := io.ReadFull(conn, user); err != nil {
			return err
		}
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return err
		}
		password := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, password); err != nil {
			return err
		}
		if header[0] != 1 || string(user) != "fixture-user" || string(password) != "fixture-pass" {
			return fmt.Errorf("incorrect proxy authentication")
		}
		if _, err := conn.Write([]byte{1, 0}); err != nil {
			return err
		}
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil {
		return err
	}
	var host string
	switch request[3] {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return err
		}
		host = net.IP(ip).String()
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return err
		}
		host = string(name)
	default:
		return fmt.Errorf("unexpected SOCKS5 address type")
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return err
	}
	if request[0] != 5 || request[1] != 1 || host != expectedHost || port[0] != 1 || port[1] != 187 {
		return fmt.Errorf("incorrect SOCKS5 destination")
	}
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 1, 187}); err != nil {
		return err
	}
	secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}})
	if err := secure.Handshake(); err != nil {
		return err
	}
	if secure.ConnectionState().ServerName != expectedSNI {
		return fmt.Errorf("incorrect TLS server name")
	}
	requestHTTP, err := http.ReadRequest(bufio.NewReader(secure))
	if err != nil {
		return err
	}
	if requestHTTP.Host != "registration.test" && requestHTTP.Host != "relay.test" {
		return fmt.Errorf("incorrect tunnel HTTP host")
	}
	_, err = secure.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	return err
}

func TestSOCKS5RegistrationAndRelay(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	for _, authenticated := range []bool{false, true} {
		t.Run(strconv.FormatBool(authenticated), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			results := make(chan error, 2)
			go func() {
				for _, host := range []string{"registration.test", "192.0.2.123"} {
					conn, err := listener.Accept()
					if err != nil {
						results <- err
						return
					}
					results <- serveSOCKS5TLS(conn, certificate, authenticated, host, "custom-tls.test")
				}
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			settings := defaultViewerSettings()
			settings.SOCKS5Enabled = true
			settings.SOCKS5Host = host
			settings.SOCKS5Port, _ = strconv.Atoi(port)
			settings.TLSServerName = "custom-tls.test"
			if authenticated {
				settings.SOCKS5Username = "fixture-user"
				settings.SOCKS5Password = "fixture-pass"
			}
			registration := newTransport(2*time.Second, true)
			registration.logID, registration.role = "fixture-connection", "registration"
			registration.settings = settings
			err = registration.connectRegistration("registration.test", 443)
			registration.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-results; err != nil {
				t.Fatal(err)
			}
			relay := newTransport(2*time.Second, true)
			relay.logID, relay.role = "fixture-connection", "relay"
			relay.settings = settings
			err = relay.connectSession("192.0.2.123", 443, "relay.test", true)
			relay.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, expected := range []string{"connection=fixture-connection transport=registration", "transport=relay", `destination="registration.test:443"`, `destination="192.0.2.123:443"`, `route="socks5://127.0.0.1:`, "proxy_auth=true", `server_name="custom-tls.test"`, "event=tls_handshake status=connected", `http_status="HTTP/1.1 200 OK"`, "event=transport_close status=closed"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("connection log missing %q", expected)
		}
	}
	if strings.Contains(logs.String(), "fixture-user") || strings.Contains(logs.String(), "fixture-pass") {
		t.Fatal("proxy credentials leaked into logs")
	}
}

func TestConnectionFailureLogsStageWithoutCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	profile, err := defaultRegistrationProfile()
	if err != nil {
		t.Fatal(err)
	}
	profile.Terms[10] = "invalid"
	session := newControlSession(time.Second, true)
	if err := session.connect(profile, makePartnerProfile("123456789", "private-partner-password")); err == nil {
		t.Fatal("invalid registration profile accepted")
	}
	if !strings.Contains(logs.String(), `status=failed stage="validate profiles"`) || !strings.Contains(logs.String(), "error=") {
		t.Fatal("failure stage missing from logs")
	}
	for _, secret := range []string{"private-partner-password", profile.Terms[1], profile.Terms[2]} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("registration credentials leaked into logs")
		}
	}
}

func TestSOCKS5HandshakeTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	tr := newTransport(50*time.Millisecond, true)
	tr.settings.SOCKS5Enabled = true
	tr.settings.SOCKS5Host = host
	tr.settings.SOCKS5Port, _ = strconv.Atoi(port)
	start := time.Now()
	conn, err := tr.dial("registration.test", 443)
	if conn != nil {
		conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "SOCKS5 proxy") {
		t.Fatal("stalled proxy did not fail")
	}
	if time.Since(start) > time.Second {
		t.Fatal("proxy handshake exceeded timeout")
	}
	<-done
}

func TestNetworkSettingsValidationAndPersistence(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.standaloneSettings = true
	v.settingsPath = t.TempDir() + "/settings.json"
	settings := v.settings
	settings.RegistrationHost = " registration.test "
	settings.TLSServerName = " custom.test "
	settings.SOCKS5Enabled = true
	settings.SOCKS5Host = " 127.0.0.1 "
	settings.SOCKS5Port = 1234
	settings.SOCKS5Username = "fixture-user"
	settings.SOCKS5Password = "fixture-pass"
	if err := v.updateSettings(settings); err != nil {
		t.Fatal(err)
	}
	settings.normalize()
	fresh := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	if err := fresh.loadSettings(v.settingsPath); err != nil {
		t.Fatal(err)
	}
	if fresh.settings != settings {
		t.Fatal("network settings did not persist")
	}
	for _, mutate := range []func(*viewerSettings){func(s *viewerSettings) { s.RegistrationHost = "https://host.test" }, func(s *viewerSettings) { s.TLSServerName = "bad\r\nname" }, func(s *viewerSettings) { s.SOCKS5Host = "" }, func(s *viewerSettings) { s.SOCKS5Port = 65536 }, func(s *viewerSettings) { s.SOCKS5Username = "" }, func(s *viewerSettings) { s.SOCKS5Password = strings.Repeat("x", 256) }} {
		invalid := settings
		mutate(&invalid)
		if err := v.updateSettings(invalid); err == nil {
			t.Fatal("invalid network settings accepted")
		}
		if v.settings != settings {
			t.Fatal("invalid settings changed state")
		}
	}
}
