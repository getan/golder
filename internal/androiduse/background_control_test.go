package androiduse

import "testing"

// Verify background control retains its independent consent gate.
func TestPhase2RequiresExplicitConsent(t *testing.T) {
	gate := NewPhase2Gate()
	if err := gate.Enable(false); err == nil {
		t.Fatal("Enable(false) must fail")
	}
	if gate.State() != Phase2Disabled {
		t.Fatalf("state = %s, want disabled", gate.State())
	}
}

func TestPhase2Lifecycle(t *testing.T) {
	gate := NewPhase2Gate()
	if err := gate.Enable(true); err != nil {
		t.Fatal(err)
	}
	if gate.State() != Phase2NeedsPairing {
		t.Fatalf("state = %s, want needs-pairing", gate.State())
	}
	if err := gate.HelperConnected(); err == nil {
		t.Fatal("helper must not connect before pairing")
	}
	gate.PairingCompleted()
	if gate.State() != Phase2NeedsHelper {
		t.Fatalf("state = %s, want needs-helper", gate.State())
	}
	if err := gate.HelperConnected(); err != nil {
		t.Fatal(err)
	}
	if gate.State() != Phase2Ready {
		t.Fatalf("state = %s, want ready", gate.State())
	}
	gate.HelperDisconnected()
	if gate.State() != Phase2NeedsHelper {
		t.Fatalf("state = %s, want needs-helper", gate.State())
	}
	gate.Stop()
	if gate.State() != Phase2Stopped {
		t.Fatalf("state = %s, want stopped", gate.State())
	}
	gate.Disable()
	if gate.State() != Phase2Disabled {
		t.Fatalf("state = %s, want disabled", gate.State())
	}
}
