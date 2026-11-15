package androiduse

import "fmt"

// Phase2State is deliberately separate from Phase 1. Enabling the advanced
// preference must never make Phase 1 tools look like they have shell access.
type Phase2State string

const (
	Phase2Disabled     Phase2State = "disabled"
	Phase2NeedsPairing Phase2State = "needs-pairing"
	Phase2NeedsHelper  Phase2State = "needs-helper"
	Phase2Ready        Phase2State = "ready"
	Phase2Stopped      Phase2State = "stopped"
)

// Phase2Gate is the policy floor for the high-privilege helper path. Android
// binding is tracked separately by Kotlin, but this state machine decides when
// the Go tool layer may consider the helper usable.
type Phase2Gate struct {
	state     Phase2State
	consented bool
	paired    bool
	helper    bool
}

func NewPhase2Gate() *Phase2Gate {
	return &Phase2Gate{state: Phase2Disabled}
}

func (g *Phase2Gate) State() Phase2State {
	if g == nil {
		return Phase2Disabled
	}
	return g.state
}

func (g *Phase2Gate) Enable(consent bool) error {
	if !consent {
		return fmt.Errorf("phase 2 requires explicit shell-risk consent")
	}
	g.consented = true
	if g.paired {
		if g.helper {
			g.state = Phase2Ready
		} else {
			g.state = Phase2NeedsHelper
		}
		return nil
	}
	g.state = Phase2NeedsPairing
	return nil
}

func (g *Phase2Gate) PairingCompleted() {
	if !g.consented {
		return
	}
	g.paired = true
	if g.helper {
		g.state = Phase2Ready
	} else {
		g.state = Phase2NeedsHelper
	}
}

func (g *Phase2Gate) HelperConnected() error {
	if !g.consented {
		return fmt.Errorf("phase 2 is not consented")
	}
	if !g.paired {
		return fmt.Errorf("phase 2 helper requires completed pairing")
	}
	g.helper = true
	g.state = Phase2Ready
	return nil
}

func (g *Phase2Gate) HelperDisconnected() {
	if g.state == Phase2Ready {
		g.helper = false
		g.state = Phase2NeedsHelper
	}
}

func (g *Phase2Gate) Stop() {
	g.helper = false
	g.state = Phase2Stopped
}

func (g *Phase2Gate) Disable() {
	g.helper = false
	g.paired = false
	g.consented = false
	g.state = Phase2Disabled
}
