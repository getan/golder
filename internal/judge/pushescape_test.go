package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// countingClassifier records whether the local table deferred a call, and
// answers like a reviewer that would contain it.
type countingClassifier struct {
	v     Verdict
	calls int
}

func (c *countingClassifier) Classify(context.Context, string, json.RawMessage) Verdict {
	c.calls++
	return c.v
}

// sshPushCall is the call shape this file is about: a push whose remote,
// transport and pre-push hook have already been resolved by gitPushFacts.
func sshPushCall(t *testing.T, escalated bool) (agentcore.AgentToolCall, string) {
	t.Helper()
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@review.example.com:org/repo.git")
	args := map[string]string{"command": "git push origin main"}
	if escalated {
		args["sandbox_permissions"] = "require_escalated"
		args["justification"] = "the push cannot succeed inside the file sandbox"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return agentcore.AgentToolCall{ID: "push", Name: "bash", Arguments: raw}, repo
}

// A fact-proven SSH push is not a caller's claim: gitPushFacts already refused
// opaque transports and unbound hooks, so the local table can keep its own tier
// instead of handing the question back to a model that may answer Contain. In
// `auto` that deferral was a dead end: Contain cannot run an escape.
func TestLocalConfirmsProvenSSHPushEvenWhenEscalationIsRequested(t *testing.T) {
	call, repo := sshPushCall(t, true)
	ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))

	deferred := &countingClassifier{v: Verdict{Level: Contain, Source: "llm"}}
	v := Local{Next: deferred}.Classify(ctx, call.Name, call.Arguments)

	if deferred.calls != 0 {
		t.Fatal("a proven SSH push was deferred to the reviewer")
	}
	if v.Failed || v.Level != Confirm || !v.Network || v.Source != LocalSource {
		t.Fatalf("verdict = %+v", v)
	}
}

// The escape request alone still buys nothing: with no derived fact, a caller
// asking to leave the sandbox goes to the reviewer, which is where conversation
// context lives.
func TestLocalStillDefersEscalationWithoutProvenFacts(t *testing.T) {
	call, _ := sshPushCall(t, true)

	deferred := &countingClassifier{v: Verdict{Level: Contain, Source: "llm"}}
	v := Local{Next: deferred}.Classify(context.Background(), call.Name, call.Arguments)

	if deferred.calls != 1 {
		t.Fatalf("deferrals = %d, want 1", deferred.calls)
	}
	if v.Level != Contain {
		t.Fatalf("verdict = %+v", v)
	}
}

// An ordinary push (no escape request) keeps working exactly as before.
func TestLocalConfirmsProvenSSHPushWithoutEscalation(t *testing.T) {
	call, repo := sshPushCall(t, false)
	ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))

	deferred := &countingClassifier{v: Verdict{Level: Contain, Source: "llm"}}
	v := Local{Next: deferred}.Classify(ctx, call.Name, call.Arguments)

	if deferred.calls != 0 {
		t.Fatal("a proven SSH push was deferred to the reviewer")
	}
	if v.Level != Confirm || !v.Network {
		t.Fatalf("verdict = %+v", v)
	}
}

// A reviewer that failed is not a reviewer that said no. The note has to send
// the reader to "retry", not to "this is forbidden".
func TestAutoFailedReviewOfSSHPushSaysRetryNotDenied(t *testing.T) {
	call, repo := sshPushCall(t, true)
	ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))

	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Cwd:        repo,
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Source: "llm", Failed: true}},
		Sandboxed:  func(string) bool { return true },
		Notify:     func(n Note) { notes = append(notes, n) },
	})
	dec := gate(ctx, call)
	if dec == nil || !dec.Block {
		t.Fatalf("decision = %+v", dec)
	}
	if !hasRationale(notes, "retry") {
		t.Fatalf("notes = %+v", notes)
	}
	if hasRationale(notes, "not authorized") {
		t.Fatalf("a failed review was reported as a refusal: %+v", notes)
	}
}

// A contained SSH push cannot run confined, so the note must name that fact
// instead of the generic "needs extra capabilities".
func TestAutoContainedSSHPushExplainsWhyItCannotRun(t *testing.T) {
	call, repo := sshPushCall(t, true)
	ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))

	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Cwd:        repo,
		Classifier: consentClassifier{v: Verdict{Level: Contain, Source: "llm"}},
		Sandboxed:  func(string) bool { return true },
		Notify:     func(n Note) { notes = append(notes, n) },
	})
	dec := gate(ctx, call)
	if dec == nil || !dec.Block || dec.Direct {
		t.Fatalf("decision = %+v", dec)
	}
	if !hasRationale(notes, "SSH") {
		t.Fatalf("notes = %+v", notes)
	}
}

func hasRationale(notes []Note, want string) bool {
	for _, n := range notes {
		if strings.Contains(n.Rationale, want) {
			return true
		}
	}
	return false
}
