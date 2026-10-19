package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// NetworkConfig is the optional policy for the network capability.
//
// Follow is ON by default, which is why it is a pointer: an absent key means
// "the default", while `follow = false` is a decision the user made. Under the
// default, an auto-mode Allow carries the network bit with it instead of asking
// a second question — the call's own review already settled the risk; a
// remembered denial still wins, and ask mode keeps its own question.
type NetworkConfig struct {
	Follow *bool `toml:"follow"`
}

// NetworkFollowEnabled resolves the policy: unset means on, and an explicit
// false means off. A config that cannot be read also means on — the setting is
// a convenience, and failing to read a preference must not turn into a silent
// change of how much gets asked.
func NetworkFollowEnabled() bool {
	cfg, err := LoadNetworkConfig()
	if err != nil || cfg.Follow == nil {
		return true
	}
	return *cfg.Follow
}

// SetNetworkFollowEnabled writes an explicit value, so "off" survives a restart.
func SetNetworkFollowEnabled(on bool) error {
	cfg, err := LoadNetworkConfig()
	if err != nil {
		return err
	}
	v := on
	cfg.Follow = &v
	return SaveNetworkConfig(cfg)
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
