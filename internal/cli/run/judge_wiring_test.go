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

// TestNewReviewerGradesEveryPatch pins the wiring after the routine-edit fast
// path was removed: no patch skips the reviewer, and one whose effect outlives
// the call reaches it like any other. The provider is nil on purpose, which
// degrades the reviewer to fail-closed — so the outcome is a failed verdict
// rather than an allow, and that is exactly what proves the reviewer was
// consulted instead of a shortcut answering.
func TestNewReviewerGradesEveryPatch(t *testing.T) {
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

	// An ordinary in-workspace patch still reaches the reviewer. There used to be
	// an exemption here; with it gone, an absent reviewer fails closed, which is
	// the safe direction and the distinguishable outcome this test relies on.
	v := classifier.Classify(context.Background(), "apply_patch", patchArgs("internal/main.go"))
	if v.Level == judge.Allow {
		t.Errorf("a routine patch was allowed without a reviewer: %+v", v)
	}
	if !v.Failed {
		t.Errorf("with no provider the verdict must fail closed, got %+v", v)
	}

	// A patch whose effect outlives the call is graded the same way: it is not
	// singled out into a shortcut, and the fact about it travels to the reviewer
	// through the state (judge.OutlivingPaths) rather than gating it here.
	v = classifier.Classify(context.Background(), "apply_patch", patchArgs(".git/hooks/pre-commit"))
	if v.Level == judge.Allow {
		t.Errorf("a patch to .git/hooks was allowed without a reviewer: %+v", v)
	}
}
