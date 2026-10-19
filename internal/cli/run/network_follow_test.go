package run

import (
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/judge"
)

func TestNewGatesLoadsNetworkFollow(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	// The shipped default is on.
	judge.SetNetworkFollow(false)
	NewGates(GatesOpts{})
	if !judge.NetworkFollow() {
		t.Fatal("NewGates should turn the policy on when config.toml says nothing")
	}
	judge.SetNetworkFollow(false)
	defer judge.SetNetworkFollow(false)
	if err := config.SetNetworkFollowEnabled(false); err != nil {
		t.Fatalf("save: %v", err)
	}
	NewGates(GatesOpts{})
	if judge.NetworkFollow() {
		t.Fatal("an explicit off should survive into the gates")
	}
}
