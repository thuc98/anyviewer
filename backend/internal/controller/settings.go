package controller

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type viewerSettings struct {
	MachineName      string `json:"machine_name"`
	JPEGQuality      int    `json:"jpeg_quality"`
	ClipboardSync    bool   `json:"clipboard_sync"`
	Theme            string `json:"theme"`
	RefreshInterval  int    `json:"refresh_interval"`
	ScreenFit        bool   `json:"screen_fit"`
	ViewOnly         bool   `json:"view_only"`
	RegistrationHost string `json:"registration_host"`
	TLSServerName    string `json:"tls_server_name"`
	SOCKS5Enabled    bool   `json:"socks5_enabled"`
	SOCKS5Host       string `json:"socks5_host"`
	SOCKS5Port       int    `json:"socks5_port"`
	SOCKS5Username   string `json:"socks5_username"`
	SOCKS5Password   string `json:"socks5_password"`
}

func defaultViewerSettings() viewerSettings {
	name, _ := os.Hostname()
	return viewerSettings{MachineName: name, JPEGQuality: 30, Theme: "light", RefreshInterval: 500, ScreenFit: true, RegistrationHost: defaultRegHost, TLSServerName: tlsTargetHost, SOCKS5Port: 1080}
}
func (s *viewerSettings) normalize() {
	s.RegistrationHost = strings.TrimSpace(s.RegistrationHost)
	s.TLSServerName = strings.TrimSpace(s.TLSServerName)
	s.SOCKS5Host = strings.TrimSpace(s.SOCKS5Host)
	if s.RegistrationHost == "" {
		s.RegistrationHost = defaultRegHost
	}
	if s.TLSServerName == "" {
		s.TLSServerName = tlsTargetHost
	}
	if s.SOCKS5Port == 0 {
		s.SOCKS5Port = 1080
	}
	if s.Theme == "" {
		s.Theme = "light"
	}
	if s.RefreshInterval == 0 {
		s.RefreshInterval = 500
	}
}
func (s viewerSettings) validate() error {
	if !validNetworkHost(s.RegistrationHost) || !validNetworkHost(s.TLSServerName) {
		return errors.New("registration host and TLS server name must be hostnames or IP addresses without a scheme or port")
	}
	if s.SOCKS5Port < 1 || s.SOCKS5Port > 65535 {
		return errors.New("SOCKS5 port must be between 1 and 65535")
	}
	if s.SOCKS5Enabled && !validNetworkHost(s.SOCKS5Host) {
		return errors.New("SOCKS5 host must be a hostname or IP address without a scheme or port")
	}
	if len(s.SOCKS5Username) > 255 || len(s.SOCKS5Password) > 255 || (s.SOCKS5Enabled && s.SOCKS5Password != "" && s.SOCKS5Username == "") {
		return errors.New("SOCKS5 credentials must be at most 255 bytes; a password requires a username")
	}
	if s.Theme != "light" && s.Theme != "dark" {
		return errors.New("unsupported UI theme")
	}
	if s.RefreshInterval != 250 && s.RefreshInterval != 500 && s.RefreshInterval != 1000 {
		return errors.New("unsupported refresh interval")
	}
	if strings.TrimSpace(s.MachineName) == "" || !utf8.ValidString(s.MachineName) || len([]rune(s.MachineName)) > 128 || strings.ContainsAny(s.MachineName, ":\x00\r\n") {
		return errors.New("machine name must contain 1–128 characters without colon or control separators")
	}
	if s.JPEGQuality != 30 && s.JPEGQuality != 45 && s.JPEGQuality != 70 {
		return errors.New("unsupported quality preset")
	}
	return nil
}

func validNetworkHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (v *Viewer) loadSettings(path string) error {
	v.settingsPath = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return v.loadSettingsData(data)
}

func (v *Viewer) loadSettingsData(data []byte) error {
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}
	settings := defaultViewerSettings()
	raw, found := profile["viewer_settings"]
	if found {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
	}
	settings.normalize()
	if err := settings.validate(); err != nil {
		return err
	}
	v.settings = settings
	return nil
}

func (v *Viewer) updateSettings(settings viewerSettings) error {
	settings.MachineName = strings.TrimSpace(settings.MachineName)
	settings.normalize()
	if err := settings.validate(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state.State == "connecting" {
		return errors.New("wait for the connection attempt to finish before saving settings")
	}
	if v.state.State == "connected" && len(v.commands) == cap(v.commands) {
		return errors.New("input queue is full")
	}
	if v.settingsPath != "" {
		v.settingsFileMu.Lock()
		defer v.settingsFileMu.Unlock()
		original, err := os.ReadFile(v.settingsPath)
		if v.standaloneSettings && os.IsNotExist(err) {
			original = []byte("{}")
			err = nil
		}
		if err != nil {
			return err
		}
		var profile map[string]json.RawMessage
		if err := json.Unmarshal(original, &profile); err != nil {
			return err
		}
		if profile == nil {
			return errors.New("invalid registration profile")
		}
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		profile["viewer_settings"] = raw
		data, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			return err
		}
		if v.standaloneSettings {
			if err := os.MkdirAll(filepath.Dir(v.settingsPath), 0700); err != nil {
				return err
			}
		}
		temp, err := os.CreateTemp(filepath.Dir(v.settingsPath), ".viewer-settings-*")
		if err != nil {
			return err
		}
		name := temp.Name()
		defer os.Remove(name)
		mode := os.FileMode(0600)
		info, err := os.Stat(v.settingsPath)
		if err == nil {
			mode = info.Mode().Perm()
		} else if !v.standaloneSettings || !os.IsNotExist(err) {
			temp.Close()
			return err
		}
		if err := temp.Chmod(mode); err != nil {
			temp.Close()
			return err
		}
		if _, err := temp.Write(data); err != nil {
			temp.Close()
			return err
		}
		if err := temp.Close(); err != nil {
			return err
		}
		if err := os.Rename(name, v.settingsPath); err != nil {
			return err
		}
	}
	if v.state.State == "connected" && settings.JPEGQuality != v.settings.JPEGQuality {
		v.commands <- cmdItem{payload: utf16le("31|||" + strconv.Itoa(settings.JPEGQuality)), unicode: true}
	}
	v.settings = settings
	if v.onSettings != nil {
		v.onSettings(settings)
	}
	if !settings.ClipboardSync {
		v.clipboard = clipboardState{}
		v.clipboardSent = ""
	}
	return nil
}
