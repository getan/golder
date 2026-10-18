package run

import (
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/judge"
)

func TestNewGatesLoadsNetworkFollow(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := config.SaveNetworkConfig(config.NetworkConfig{Follow: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	judge.SetNetworkFollow(false)
	defer judge.SetNetworkFollow(false)
	NewGates(GatesOpts{})
	if !judge.NetworkFollow() {
		t.Fatal("NewGates did not load the persisted follow policy")
	}
}
