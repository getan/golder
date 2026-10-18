package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// NetworkConfig is the optional policy for the network capability. Follow is
// off by default: the network stays a separate per-call decision. When it is on,
// an auto-mode Allow carries the network bit with it instead of asking again;
// a remembered denial still wins, and ask mode keeps its own question.
type NetworkConfig struct {
	Follow bool `toml:"follow"`
}

// NetworkConfigPath is the network policy file beside config.toml, kept
// separate for the same reason proxy.toml is: /network must never rewrite
// credentials or unrelated settings.
func NetworkConfigPath() string {
	if path := FileConfigPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "network.toml")
	}
	return ""
}

// LoadNetworkConfig reads network.toml. A missing file is the default policy.
func LoadNetworkConfig() (NetworkConfig, error) {
	path := NetworkConfigPath()
	if path == "" {
		return NetworkConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NetworkConfig{}, nil
	}
	if err != nil {
		return NetworkConfig{}, fmt.Errorf("cannot read network settings")
	}
	var cfg NetworkConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return NetworkConfig{}, fmt.Errorf("invalid network.toml; check follow")
	}
	return cfg, nil
}

// SaveNetworkConfig writes network.toml atomically.
func SaveNetworkConfig(cfg NetworkConfig) error {
	path := NetworkConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate network settings directory")
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("cannot encode network settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create network settings directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".network-*.toml")
	if err != nil {
		return fmt.Errorf("cannot create network settings file")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("cannot write network settings")
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close network settings")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("cannot save network settings")
	}
	return nil
}
