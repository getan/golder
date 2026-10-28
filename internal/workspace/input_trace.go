package workspace

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/getan/golder/internal/diagtrace"
)

// observeWorkspaceInput uses the same metadata-only seam as the agent TUI.
// It does not wrap/read stdin, change terminal modes or log key/paste contents.
func observeWorkspaceInput(trace *diagtrace.Trace) func() {
	if trace == nil {
		return func() {}
	}
	return uv.SetInputObserver(func(e uv.InputObservation) {
		trace.Event("workspace.input", "%s bytes=%d events=%d consumed=%d type=%s failed=%t duration=%s", e.Phase, e.Bytes, e.Events, e.Consumed, e.Type, e.Failed, e.Duration)
	})
}
