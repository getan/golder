package repl

// Tests for the REPL's interrupt wording: an interrupted run reports the same
// single line as the TUI (ui.InterruptNotice), and it says it exactly once —
// the aborted-turn branch and the driver's post-drain ctx check describe the
// same keystroke, so before the dedupe the REPL printed it twice.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/provider"
)

// abortProvider streams one turn whose stop reason is aborted — the shape a
// cancelled provider request produces.
type abortProvider struct{}

func (abortProvider) Name() string { return "faux" }
func (abortProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "faux", ID: "faux"}}
}

func (abortProvider) StreamCompletion(ctx context.Context, req provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	final := partial
	final.StopReason = agentcore.StopReasonAborted
	s := provider.NewAssistantMessageEventStream(0)
	go func() {
		// Emitted on a background context so the events survive a cancelled run,
		// exactly like the transport's own cancel path (which uses
		// context.Background() for the same reason).
		_ = s.Emit(context.Background(), provider.StreamStartEvent{Partial: partial})
		_ = s.Emit(context.Background(), provider.StreamDoneEvent{Message: final})
		s.Close()
	}()
	return s, nil
}

// TestStreamRunInterruptNoticeOnce: an aborted turn prints the shared interrupt
// line once. The turn-end branch is the one that reports it (the run's ctx is
// still live), and the driver's ctx check stays quiet because nothing was
// cancelled.
func TestStreamRunInterruptNoticeOnce(t *testing.T) {
	deps, _ := newTestDeps(t, abortProvider{})
	var out bytes.Buffer
	streamRun(context.Background(), &out, deps, "hi")
	if got := strings.Count(out.String(), ui.InterruptNotice); got != 1 {
		t.Errorf("interrupt notice printed %d time(s), want 1:\n%s", got, out.String())
	}
	if strings.Contains(out.String(), "aborted") {
		t.Errorf("an interrupt must not be reported as an error/abort line:\n%s", out.String())
	}
}

// TestStreamRunCancelledCtxPrintsOnce: with the run's ctx already cancelled the
// driver's own check reports the interrupt — once, not once per reporting site.
func TestStreamRunCancelledCtxPrintsOnce(t *testing.T) {
	deps, _ := newTestDeps(t, abortProvider{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	streamRun(ctx, &out, deps, "hi")
	if got := strings.Count(out.String(), ui.InterruptNotice); got != 1 {
		t.Errorf("interrupt notice printed %d time(s), want 1:\n%s", got, out.String())
	}
}
