package config

// The managed permissions store: ~/.config/golder/permissions.toml. It holds
// the sandbox writable-path grants the user accumulated from approval dialogs
// and /permissions writable, in its own file for the same reason proxy.toml
// exists: rewriting config.toml would drop the user's comments and, worse,
// re-serialize the credential reference next to the rest of the settings. The
// [permissions] table in config.toml stays the hand-edited seed; startup
// merges both (config.toml first, then this file, deduplicated).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// PermissionsConfigPath returns ~/.config/golder/permissions.toml (honoring
// $XDG_CONFIG_HOME), or "" when no config directory can be resolved.
func PermissionsConfigPath() string {
	if path := FileConfigPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "permissions.toml")
	}
	return ""
}

// LoadPermissionsConfig reads the managed store. A missing file (or an empty
// path) is a zero config with no error; a malformed file is an error the
// caller warns about rather than aborting.
func LoadPermissionsConfig() (PermissionsConfig, error) {
	path := PermissionsConfigPath()
	if path == "" {
		return PermissionsConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PermissionsConfig{}, nil
	}
	if err != nil {
		return PermissionsConfig{}, fmt.Errorf("cannot read permissions settings")
	}
	var cfg PermissionsConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return PermissionsConfig{}, fmt.Errorf("invalid permissions.toml; check writable_roots")
	}
	return cfg, nil
}

// SavePermissionsConfig writes the managed store atomically (temp file +
// rename) with directory mode 0700, mirroring SaveProxyConfig.
func SavePermissionsConfig(cfg PermissionsConfig) error {
	path := PermissionsConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate permissions settings directory")
	}
	if cfg.WritableRoots == nil {
		cfg.WritableRoots = []string{}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("cannot encode permissions settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("cannot create permissions settings directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".permissions-*.toml")
	if err != nil {
		return fmt.Errorf("cannot create permissions settings file")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("cannot write permissions settings")
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close permissions settings")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("cannot save permissions settings")
	}
	return nil
}
