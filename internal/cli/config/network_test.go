package config

import "testing"

func TestNetworkConfigRoundTrip(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := SaveNetworkConfig(NetworkConfig{Follow: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := LoadNetworkConfig()
	if err != nil || !cfg.Follow {
		t.Fatalf("load = %+v, %v", cfg, err)
	}
	if err := SaveNetworkConfig(NetworkConfig{Follow: false}); err != nil {
		t.Fatalf("save off: %v", err)
	}
	cfg, err = LoadNetworkConfig()
	if err != nil || cfg.Follow {
		t.Fatalf("load off = %+v, %v", cfg, err)
	}
}
