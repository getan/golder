package run

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
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

// TestNewReviewerCarriesTheWorkspaceRoot pins the wiring the routine-edit fast
// path depends on: NewReviewer must hand its cwd to the Chain, or the exemption
// silently stays off in production and every patch pays the review again. The
// provider is nil on purpose — that degrades the reviewer to fail-closed, which
// makes the two outcomes distinguishable without a model: a routine patch still
// comes back as an allow from the fast path, while a protected one comes back
// failed (the reviewer could not run).
func TestNewReviewerCarriesTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	classifier := NewReviewer("m", "opencode-go", nil, nil, "", root, true)

	patchArgs := func(p string) []byte {
		raw, err := json.Marshal(map[string]string{
			"patch": "*** Begin Patch\n*** Add File: " + p + "\n+x\n*** End Patch\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// A routine in-workspace patch: the fast path answers, so the absent
	// reviewer is never consulted.
	v := classifier.Classify(context.Background(), "apply_patch", patchArgs("internal/main.go"))
	if v.Level != judge.Allow || v.Source != "workspace-edit" {
		t.Errorf("routine patch verdict = %+v, want the workspace-edit fast path "+
			"(an empty WorkspaceRoot on the Chain would leave the reviewer to answer)", v)
	}

	// A protected path reaches the reviewer, and this classifier has none — so
	// the contrast is what proves the root was actually used.
	v = classifier.Classify(context.Background(), "apply_patch", patchArgs(".git/hooks/pre-commit"))
	if v.Source == "workspace-edit" {
		t.Error("a patch to .git/hooks took the fast path; the guard is not reachable from the production wiring")
	}
}
