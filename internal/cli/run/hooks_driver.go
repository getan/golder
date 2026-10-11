// This file holds chainEvent, the observer composition the gate assembly uses
// (#425): the SessionEnd/PreCompact hook notifier is chained onto whatever
// event seam the driver owns (HeadlessConfig.OnEvent, the DrainStream handler
// for the REPL/TUI).
//
// The tool-execution and Stop seams, the SessionStart dispatch and that
// notifier used to be installed here by InstallDriverHooks, one of two
// entry points (#425 converged the six drivers onto it). Both entry points are
// gone: run.Gates.InstallDriver is now the single one, so a driver cannot wire
// its gates without also wiring the hook seams they belong beside.
package run

import (
	"github.com/getan/golder/internal/agentcore"
)

// chainEvent composes two AgentEvent observers into one that calls prev then
// next. A nil operand is identity, so chaining onto an unset seam returns the
// other unchanged (and returns nil when both are nil, keeping the seam unset).
func chainEvent(prev, next func(agentcore.AgentEvent)) func(agentcore.AgentEvent) {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ev agentcore.AgentEvent) {
		prev(ev)
		next(ev)
	}
}
