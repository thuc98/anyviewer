package controller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type viewerSettings struct {
	MachineName     string `json:"machine_name"`
	JPEGQuality     int    `json:"jpeg_quality"`
	ClipboardSync   bool   `json:"clipboard_sync"`
	Theme           string `json:"theme"`
	RefreshInterval int    `json:"refresh_interval"`
	ScreenFit       bool   `json:"screen_fit"`
	ViewOnly        bool   `json:"view_only"`
}

func defaultViewerSettings() viewerSettings {
	name, _ := os.Hostname()
	return viewerSettings{MachineName: name, JPEGQuality: 30, Theme: "light", RefreshInterval: 500, ScreenFit: true}
}
func (s *viewerSettings) normalize() {
	if s.Theme == "" {
		s.Theme = "light"
	}
	if s.RefreshInterval == 0 {
		s.RefreshInterval = 500
	}
}
func (s viewerSettings) validate() error {
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

// Viewer preferences live inside the registration profile. Protocol fields remain intact.
func (v *Viewer) loadSettings(path string) error {
	v.settingsPath = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}
	settings := defaultViewerSettings()
	raw, found := profile["viewer_settings"]
	if !found {
		// Preserve previously saved preferences on upgrade; the next save migrates them.
		raw, err = os.ReadFile(filepath.Join(filepath.Dir(path), "viewer-settings.json"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return err
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
		temp, err := os.CreateTemp(filepath.Dir(v.settingsPath), ".viewer-settings-*")
		if err != nil {
			return err
		}
		name := temp.Name()
		defer os.Remove(name)
		info, err := os.Stat(v.settingsPath)
		if err != nil {
			temp.Close()
			return err
		}
		if err := temp.Chmod(info.Mode().Perm()); err != nil {
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
