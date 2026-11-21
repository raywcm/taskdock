package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/raywcm/taskdock/internal/config"
)

func TestSettingsRoundTripPermissionsAndValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := s.Settings()
	if err != nil || defaults != config.Defaults() {
		t.Fatalf("defaults: %+v %v", defaults, err)
	}
	changed := defaults
	changed.Language = "zh-CN"
	changed.Theme, changed.ColorMode = "terminal", "light"
	changed.DefaultTab = "stop"
	changed.ShowResources = false
	if err := s.SaveSettings(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	again, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := again.Settings()
	if err != nil || loaded != changed {
		t.Fatalf("preferences did not survive reopen: %+v %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(root, "settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings not private")
	}
	invalid := changed
	invalid.RefreshSeconds = 0
	if err := s.SaveSettings(context.Background(), invalid); err == nil {
		t.Fatal("accepted invalid interval")
	}
	loaded, _ = again.Settings()
	if loaded != changed {
		t.Fatal("invalid update damaged saved preferences")
	}
}

func TestOldPreferencesUseDefaultRetention(t *testing.T) {
	repository, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Root(), "settings.json"), []byte(`{"schema_version":1,"language":"en"}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := repository.Settings()
	if err != nil || settings.HistoryRetentionSeconds != 3600 {
		t.Fatal("old settings lost default retention", settings, err)
	}
}

func TestLegacyAppearanceMigratesOnNextEdit(t *testing.T) {
	for _, tc := range []struct{ file, theme, mode string }{
		{`{}`, "terminal", "auto"},
		{`{"theme":"classic","color_mode":"light"}`, "classic", "light"},
		{`{"theme":"auto"}`, "terminal", "auto"},
		{`{"theme":"dark"}`, "terminal", "dark"},
		{`{"theme":"light"}`, "terminal", "light"},
		{`{"theme":"terminal"}`, "terminal", "auto"},
		{`{"theme":"dark","color_mode":"auto"}`, "terminal", "auto"},
	} {
		s, err := Open(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(s.Root(), "settings.json")
		if err := os.WriteFile(path, []byte(tc.file), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := s.Settings()
		if err != nil || got.Theme != tc.theme || got.ColorMode != tc.mode {
			t.Fatalf("%s decoded as %+v: %v", tc.file, got, err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != tc.file {
			t.Fatalf("read changed %s: %q, %v", tc.file, data, err)
		}
		_, err = s.UpdateSettings(t.Context(), func(current config.Settings) (config.Settings, error) {
			return config.With(current, "refresh", "5")
		})
		if err != nil {
			t.Fatal(err)
		}
		var disk struct {
			Theme string
			Mode  string `json:"color_mode"`
		}
		if err := readJSON(path, &disk); err != nil || disk.Theme != tc.theme || disk.Mode != tc.mode {
			t.Fatalf("%s saved as %+v: %v", tc.file, disk, err)
		}
	}
}

func TestPartialSettingsEditPreservesCompatibleFieldsAndRejectsAbortedWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"extra":{"id":9007199254740993},"theme":"light"}`), 0600); err != nil {
		t.Fatal(err)
	}
	edit := func(current config.Settings) (config.Settings, error) {
		return config.With(current, "theme", "terminal")
	}
	saved, err := s.UpdateSettings(t.Context(), edit)
	if err != nil || saved.Theme != "terminal" || saved.ColorMode != "light" {
		t.Fatalf("theme update: %+v %v", saved, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct{ Extra struct{ ID uint64 } }
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Extra.ID != 9007199254740993 {
		t.Fatalf("lost extension: %s %v", data, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.UpdateSettings(ctx, edit); !errors.Is(err, context.Canceled) {
		t.Fatal("saved a cancelled edit", err)
	}
	if _, err := s.UpdateSettings(t.Context(), func(current config.Settings) (config.Settings, error) {
		current.RefreshSeconds = -1
		return current, nil
	}); err == nil {
		t.Fatal("saved invalid preferences")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(data) {
		t.Fatal("unsuccessful edits changed the file", err)
	}
}
