package appsession

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
)

const displaySettingsVersion = 1

type DisplaySettings struct {
	Version   int     `json:"version"`
	Theme     string  `json:"theme"`
	FontScale float32 `json:"font_scale"`
}

func DefaultDisplaySettings() DisplaySettings {
	return DisplaySettings{Version: displaySettingsVersion, Theme: "system", FontScale: 1.06}
}

func LoadDisplaySettings() (DisplaySettings, error) {
	defaults := DefaultDisplaySettings()
	path := golderhome.File(golderhome.GUIDisplayFile)
	if path == "" {
		return defaults, errors.New("Golder state root unavailable")
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaults, nil
	}
	if err != nil {
		return defaults, err
	}
	if len(raw) > 64<<10 {
		return defaults, errors.New("GUI display settings exceed the size limit")
	}
	var settings DisplaySettings
	if err := json.Unmarshal(raw, &settings); err != nil || settings.Version != displaySettingsVersion {
		return defaults, errors.New("GUI display settings are invalid")
	}
	if err := validateDisplaySettings(settings); err != nil {
		return defaults, err
	}
	return settings, nil
}

func SaveDisplaySettings(settings DisplaySettings) error {
	settings.Version = displaySettingsVersion
	settings.Theme = strings.ToLower(strings.TrimSpace(settings.Theme))
	if err := validateDisplaySettings(settings); err != nil {
		return err
	}
	path := golderhome.File(golderhome.GUIDisplayFile)
	if path == "" {
		return errors.New("Golder state root unavailable")
	}
	if err := privatepath.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gui-display-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func validateDisplaySettings(settings DisplaySettings) error {
	switch settings.Theme {
	case "system", "light", "dark":
	default:
		return errors.New("GUI theme must be system, light or dark")
	}
	if settings.FontScale < .85 || settings.FontScale > 1.35 {
		return errors.New("GUI font scale must be between 85% and 135%")
	}
	return nil
}
