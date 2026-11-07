package agenttool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

type changingTerminalPolicyRunner struct {
	path    string
	cleaned bool
}

func (r *changingTerminalPolicyRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	if err := os.Setenv("GOLDER_SANDBOX_READABLE", r.path); err != nil {
		return nil, nil, err
	}
	return argv, func() { r.cleaned = true }, nil
}

func TestGUITerminalGuardKeepsPersistentSandboxCeiling(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewControlGuard(root)
	runner := &controlRunner{}
	g.Runner = runner
	ctx := agentcore.WithExternalToolAuthorization(context.Background())
	if _, err := g.PrepareGUITerminal(context.Background(), nil, false); err == nil {
		t.Fatal("an ungated call could create a terminal")
	}
	if _, err := g.PrepareGUITerminal(agentcore.WithReadGrant(ctx, "/outside"), nil, false); err == nil {
		t.Fatal("one-call read grant became a persistent terminal grant")
	}
	if _, err := g.PrepareGUITerminal(ctx, nil, true); err == nil {
		t.Fatal("terminal gained network without authorization")
	}
	envelope, err := g.PrepareGUITerminal(agentcore.WithNetworkGrant(ctx), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Cleanup()
	if !envelope.Isolated || !envelope.Network || !envelope.Shell || !runner.policy.OwnTerminal || !runner.policy.Network {
		t.Fatalf("launch envelope=%+v policy=%+v", envelope, runner.policy)
	}
	if err := g.CheckGUITerminalInput(ctx, envelope); err == nil {
		t.Fatal("network-enabled terminal accepted input without network grant")
	}
	if err := g.CheckGUITerminalControl(ctx, envelope); err != nil {
		t.Fatalf("layout change incorrectly needed network: %v", err)
	}
	if err := g.CheckGUITerminalInput(agentcore.WithNetworkGrant(ctx), envelope); err != nil {
		t.Fatal(err)
	}
	stale := envelope
	stale.Policy[0] ^= 1
	if err := g.CheckGUITerminalInput(agentcore.WithNetworkGrant(ctx), stale); err == nil {
		t.Fatal("stale sandbox policy was accepted")
	}
	g.Mode.Set(permissions.ReadOnly)
	if err := g.CheckGUITerminalInput(agentcore.WithNetworkGrant(ctx), envelope); err == nil {
		t.Fatal("read-only mode accepted input")
	}
}

func TestGUITerminalFullAccessReportsActualNetworkCeiling(t *testing.T) {
	t.Setenv("GOLDER_SANDBOX", "off")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewControlGuard(root)
	g.Mode.Set(permissions.FullAccess)
	envelope, err := g.PrepareGUITerminal(agentcore.WithExternalToolAuthorization(context.Background()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Cleanup()
	if envelope.Isolated || !envelope.Network {
		t.Fatalf("unconfined terminal misreported its network ceiling: %+v", envelope)
	}
}

func TestGUITerminalRejectsPolicyChangeDuringLaunch(t *testing.T) {
	t.Setenv("GOLDER_SANDBOX_READABLE", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := NewControlGuard(root)
	runner := &changingTerminalPolicyRunner{path: t.TempDir()}
	g.Runner = runner
	if _, err := g.PrepareGUITerminal(agentcore.WithExternalToolAuthorization(context.Background()), nil, false); err == nil {
		t.Fatal("terminal accepted a sandbox policy changed during setup")
	}
	if !runner.cleaned {
		t.Fatal("failed terminal setup leaked its sandbox cleanup")
	}
}
