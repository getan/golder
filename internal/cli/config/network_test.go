package config

import "testing"

func TestNetworkConfigRoundTrip(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	// Unset means on: the policy ships enabled.
	if !NetworkFollowEnabled() {
		t.Fatal("an unset policy should be on")
	}
	if err := SetNetworkFollowEnabled(false); err != nil {
		t.Fatalf("save off: %v", err)
	}
	if NetworkFollowEnabled() {
		t.Fatal("an explicit false should be off")
	}
	cfg, err := LoadNetworkConfig()
	if err != nil || cfg.Follow == nil || *cfg.Follow {
		t.Fatalf("load off = %+v, %v", cfg, err)
	}
	if err := SetNetworkFollowEnabled(true); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !NetworkFollowEnabled() {
		t.Fatal("an explicit true should be on")
	}
	cfg, err = LoadNetworkConfig()
	if err != nil || cfg.Follow == nil || !*cfg.Follow {
		t.Fatalf("load = %+v, %v", cfg, err)
	}
}
