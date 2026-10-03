package run

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// TestReviewCheckCarriesSession verifies the reviewer's provider request
// carries the session id (opencode's x-opencode-session) exactly like the
// main loop's requests, so sticky routing accepts it.
func TestReviewCheckCarriesSession(t *testing.T) {
	var gotExtra map[string]any
	stream := func(_ context.Context, _ string, _ provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotExtra = cfg.Extra
		s := provider.NewAssistantMessageEventStream(0)
		final := agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			StopReason: agentcore.StopReasonEndTurn,
			Content:    agentcore.ContentList{agentcore.NewTextContent(`{"level":"allow","rationale":"ok"}`)},
		}
		s.SetResult(final)
		s.Close()
		return s, nil
	}
	check := ReviewCheck("m", "opencode-go", stream, nil, "sess-1")
	out, err := check(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if out != `{"level":"allow","rationale":"ok"}` {
		t.Fatalf("out = %q", out)
	}
	if got := provider.SessionHeaderValue("opencode-go", gotExtra); got != "sess-1" {
		t.Fatalf("session header value = %q, want sess-1 (extra %v)", got, gotExtra)
	}
}

// TestReviewCheckFallsBackSession verifies an empty session id still yields a
// stable process id rather than a headerless request the opencode endpoint
// rejects.
func TestReviewCheckFallsBackSession(t *testing.T) {
	var gotExtra map[string]any
	stream := func(_ context.Context, _ string, _ provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotExtra = cfg.Extra
		s := provider.NewAssistantMessageEventStream(0)
		s.SetResult(agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn})
		s.Close()
		return s, nil
	}
	check := ReviewCheck("m", "opencode-go", stream, nil, "")
	if _, err := check(context.Background(), "sys", "user"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := provider.SessionHeaderValue("opencode-go", gotExtra); got == "" {
		t.Fatalf("session header empty for a derived process id: %v", gotExtra)
	}
}
