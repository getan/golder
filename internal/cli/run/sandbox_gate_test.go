package run

import (
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/seatbelt"
)

// TestSandboxGateModeOff verifies the predicate disables itself when isolation
// is switched off, so the judge gate keeps failing Sandbox tiers closed rather
// than passing them to an execution layer that would run them unisolated.
func TestSandboxGateModeOff(t *testing.T) {
	t.Setenv("PIGO_SANDBOX", "off")
	if gate := SandboxGate(); gate != nil {
		t.Fatal("PIGO_SANDBOX=off must yield a nil predicate")
	}
}

// TestSandboxGateMatchesWiring is the consistency guarantee behind the
// pass-through: the gate says "yes, this will be isolated" only when
// WireBashSandbox actually attached a runner to the bash tool. A mismatch in
// either direction is a security bug — the gate would wave through a call the
// executor runs bare, or block a call it could have isolated.
func TestSandboxGateMatchesWiring(t *testing.T) {
	t.Setenv("PIGO_SANDBOX", "enforce")
	tools := []agentcore.AgentTool{&agenttool.BashTool{Dir: t.TempDir()}}
	WireBashSandbox(tools, t.TempDir())
	bash, ok := tools[0].(*agenttool.BashTool)
	if !ok {
		t.Fatal("expected the bash tool back")
	}

	gate := SandboxGate()
	runnerAttached := bash.Sandbox != nil
	if runnerAttached != (gate != nil) {
		t.Fatalf("runner attached = %v but SandboxGate() = %v (platform sandbox-exec available: %v)",
			runnerAttached, gate != nil, seatbelt.Available())
	}
	if gate == nil {
		return // no sandbox on this platform; nothing further to check
	}
	if !gate("bash") {
		t.Error("predicate must accept bash")
	}
	for _, name := range []string{"read", "apply_patch", "grep"} {
		if gate(name) {
			t.Errorf("predicate must not accept %s: no runner exists for it", name)
		}
	}
}
