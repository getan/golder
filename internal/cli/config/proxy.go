package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ProxyConfig is kept separately so /proxy never rewrites credentials or
// unrelated settings/comments in config.toml. A nil Providers list retains
// legacy routing; an explicit empty list means no providers use the proxy.
type ProxyConfig struct {
	URL       string   `toml:"url"`
	Providers []string `toml:"providers"`
}

func ProxyConfigPath() string {
	if path := FileConfigPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "proxy.toml")
	}
	return ""
}

func LoadProxyConfig() (ProxyConfig, error) {
	path := ProxyConfigPath()
	if path == "" {
		return ProxyConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ProxyConfig{}, nil
	}
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("cannot read proxy settings")
	}
	var cfg ProxyConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		// Parser errors may include source text containing proxy credentials.
		return ProxyConfig{}, fmt.Errorf("invalid proxy.toml; check url and providers")
	}
	return cfg, nil
}

func SaveProxyConfig(cfg ProxyConfig) error {
	path := ProxyConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate proxy settings directory")
	}
	if cfg.Providers == nil {
		cfg.Providers = []string{}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("cannot encode proxy settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("cannot create proxy settings directory")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proxy-*.toml")
	if err != nil {
		return fmt.Errorf("cannot create proxy settings file")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("cannot write proxy settings")
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close proxy settings")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("cannot save proxy settings")
	}
	return nil
}
