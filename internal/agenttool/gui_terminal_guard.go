package agenttool

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// GUITerminalEnvelope carries the actual launch argv and policy ceiling of one
// Agent-created GUI terminal. Cleanup belongs to the process and runs on exit.
type GUITerminalEnvelope struct {
	Argv     []string
	Cleanup  func()
	Isolated bool
	Network  bool
	Shell    bool
	Policy   [32]byte
}

// PrepareGUITerminal uses the same scrubbed, bounded launcher as tuios helpers.
// A one-call path grant cannot be turned into a persistent shell capability.
func (g *ControlGuard) PrepareGUITerminal(ctx context.Context, argv []string, network bool) (GUITerminalEnvelope, error) {
	if g == nil || !agentcore.ExternalToolAuthorized(ctx) {
		return GUITerminalEnvelope{}, errors.New("terminal: an authorized tool call is required")
	}
	cwd, err := g.cwd("")
	if err != nil {
		return GUITerminalEnvelope{}, err
	}
	if filepath.Clean(cwd) != filepath.Clean(g.Root) {
		return GUITerminalEnvelope{}, errors.New("terminal: project identity changed before launch")
	}
	policy := controlPolicyStamp()
	wrapped, cleanup, isolated, err := g.launchArgv(ctx, argv, cwd, network)
	if err != nil {
		return GUITerminalEnvelope{}, err
	}
	if policy != controlPolicyStamp() {
		if cleanup != nil {
			cleanup()
		}
		return GUITerminalEnvelope{}, errors.New("terminal: sandbox policy changed during launch; create a new terminal")
	}
	// An unconfined full-access shell has network even if the request said off;
	// record the actual ceiling rather than claiming a restriction it lacks.
	return GUITerminalEnvelope{Argv: wrapped, Cleanup: cleanup, Isolated: isolated, Network: network || !isolated, Shell: len(argv) == 0, Policy: policy}, nil
}

// CheckGUITerminalInput revalidates a persistent envelope immediately before
// input, including live mode and policy changes since the pane was created.
func (g *ControlGuard) CheckGUITerminalInput(ctx context.Context, envelope GUITerminalEnvelope) error {
	return g.checkGUITerminal(ctx, envelope, true)
}

// CheckGUITerminalControl checks layout/lifecycle actions without treating
// them as network input to an existing pane.
func (g *ControlGuard) CheckGUITerminalControl(ctx context.Context, envelope GUITerminalEnvelope) error {
	return g.checkGUITerminal(ctx, envelope, false)
}

func (g *ControlGuard) checkGUITerminal(ctx context.Context, envelope GUITerminalEnvelope, input bool) error {
	if g == nil || !agentcore.ExternalToolAuthorized(ctx) {
		return errors.New("terminal: an authorized tool call is required")
	}
	if g.Mode != nil && g.Mode.Mode() == permissions.ReadOnly {
		return errors.New("terminal: read-only mode cannot send input")
	}
	if envelope.Policy != controlPolicyStamp() {
		return errors.New("terminal: sandbox policy changed; create a new Agent terminal")
	}
	if input && envelope.Network && !g.network(ctx) {
		return errors.New("terminal: this pane has network access; input requires reviewed network authority")
	}
	if !envelope.Isolated && (agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce) {
		return errors.New("terminal: this pane cannot satisfy required OS containment")
	}
	if !envelope.Isolated && !g.full() {
		return errors.New("terminal: this Agent pane was created outside the sandbox and is no longer in full-access mode")
	}
	return nil
}
