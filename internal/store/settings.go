package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raywcm/taskdock/internal/config"
)

func readSettingsFile[T any](root string, defaults T) (T, error) {
	value := defaults
	dest := &value // A null top-level object must not silently restore defaults.
	err := readJSON(filepath.Join(root, "settings.json"), &dest)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err == nil && dest == nil {
		err = errors.New("expected a JSON object")
	}
	if err != nil {
		return defaults, fmt.Errorf("settings.json: %w", err)
	}
	return value, nil
}

func ReadSettings(root string) (config.Settings, error) {
	settings, err := readSettingsFile(root, config.Defaults())
	if err != nil {
		return settings, err
	}
	return settings, settings.Validate()
}
func (s *JSON) Settings() (config.Settings, error) { return ReadSettings(s.root) }

// Keep additional fields from compatible clients when editing a preferences file.
// Only writes need both the typed settings and the original JSON object.
type settingsDocument struct {
	config.Settings
	fields map[string]json.RawMessage
}

func (d *settingsDocument) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &d.Settings); err != nil {
		return err
	}
	return json.Unmarshal(data, &d.fields)
}

func (d settingsDocument) MarshalJSON() ([]byte, error) {
	known, err := json.Marshal(d.Settings)
	if err != nil || d.fields == nil {
		return known, err
	}
	if err := json.Unmarshal(known, &d.fields); err != nil {
		return nil, err
	}
	return json.Marshal(d.fields)
}

func (s *JSON) UpdateSettings(ctx context.Context, edit config.SettingsEdit) (config.Settings, error) {
	release, err := s.Lock(ctx)
	if err != nil {
		return config.Settings{}, err
	}
	defer release()
	doc, err := readSettingsFile(s.root, settingsDocument{Settings: config.Defaults()})
	if err != nil {
		return config.Settings{}, err
	}
	if err := doc.Settings.Validate(); err != nil {
		return config.Settings{}, err
	}
	next, err := edit(doc.Settings)
	if err != nil {
		return config.Settings{}, err
	}
	if err := ctx.Err(); err != nil {
		return config.Settings{}, err
	}
	if err := next.Validate(); err != nil {
		return config.Settings{}, err
	}
	doc.Settings = next
	if err := writeJSON(filepath.Join(s.root, "settings.json"), doc); err != nil {
		return config.Settings{}, err
	}
	return next, nil
}

// SaveSettings replaces the entire file, including when resetting unreadable preferences.
func (s *JSON) SaveSettings(ctx context.Context, settings config.Settings) error {
	release, err := s.Lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.root, "settings.json"), settings)
}
