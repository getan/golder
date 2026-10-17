package provider

import (
	"os"
	"testing"
)

// TestMain makes the package hermetic: GOLDER_HOME points at an empty temp dir
// so no test reads or writes a developer's real ~/.golder caches (reasoning
// catalog, model catalog, credentials). Tests that exercise the disk-cache
// paths set their own GOLDER_HOME and restore to this one on cleanup.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "golder-provider-test-")
	if err != nil {
		os.Exit(m.Run())
	}
	os.Setenv("GOLDER_HOME", dir)
	// No test may pay for the lazy wire probe: it sends real requests, and the
	// registry's gateways would reach the network. Tests that exercise the
	// probe set the variable themselves.
	os.Setenv("GOLDER_WIRE_PROBE", "off")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
