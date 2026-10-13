package controller

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPersistenceAndLiveQuality(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	path := filepath.Join(t.TempDir(), "controller-config.json")
	if err := os.WriteFile(path, []byte(`{"terms":["3","fixture-identity"],"ansi_codec":"cp1252","unknown":{"preserve":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.loadSettings(path); err != nil {
		t.Fatal(err)
	}
	v.state.State = "connected"
	settings := viewerSettings{MachineName: "Support Mac", JPEGQuality: 70, Theme: "dark", RefreshInterval: 500}
	if err := v.updateSettings(settings); err != nil {
		t.Fatal(err)
	}
	command := <-v.commands
	// Native Form1.ChangeDisplayQuality at 0x6e85b0 uses Unicode command 31.
	if !command.unicode || !bytes.Equal(command.payload, utf16le("31|||70")) {
		t.Fatal("quality setting did not send native command")
	}
	fresh := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	if err := fresh.loadSettings(path); err != nil {
		t.Fatal(err)
	}
	if fresh.settings != settings {
		t.Fatal("settings were not persisted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var profile map[string]json.RawMessage
	json.Unmarshal(data, &profile)
	if !bytes.Contains(profile["terms"], []byte("fixture-identity")) || !bytes.Contains(profile["unknown"], []byte("preserve")) || profile["viewer_settings"] == nil {
		t.Fatal("profile fields lost while saving settings")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("unexpected settings permissions")
	}
}

func TestInvalidSettingsDoNotModifyStateOrDisk(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	path := filepath.Join(t.TempDir(), "controller-config.json")
	v.loadSettings(path)
	before := v.settings
	for _, value := range []viewerSettings{{MachineName: "", JPEGQuality: 30}, {MachineName: "bad:name", JPEGQuality: 45}, {MachineName: "ok", JPEGQuality: 100}} {
		if err := v.updateSettings(value); err == nil {
			t.Fatal("accepted invalid settings")
		}
		if v.settings != before {
			t.Fatal("invalid settings changed state")
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid settings wrote disk")
	}
	v.state.State = "connecting"
	if err := v.updateSettings(viewerSettings{MachineName: "test", JPEGQuality: 45}); err == nil {
		t.Fatal("updated settings during handshake")
	}
}

func TestSettingsQueueFullDoesNotSave(t *testing.T) {
	v := newViewer(registrationProfile{}, nil, 1, 1, 32, true)
	v.settingsPath = filepath.Join(t.TempDir(), "viewer-settings.json")
	v.state.State = "connected"
	for len(v.commands) < cap(v.commands) {
		v.commands <- cmdItem{}
	}
	if err := v.updateSettings(viewerSettings{MachineName: "new", JPEGQuality: 45}); err == nil {
		t.Fatal("settings accepted with full command queue")
	}
	if _, err := os.Stat(v.settingsPath); !os.IsNotExist(err) {
		t.Fatal("saved settings without reserving a command slot")
	}
}
