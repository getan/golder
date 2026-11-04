package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestHTTPSGitCPushGetsBoundDirectEnvelopeOnFirstAttempt(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://gitlab.example.com/team/project.git")
	cwd := filepath.Dir(repo)
	command := "git -C " + filepath.Base(repo) + " push origin main"
	args, err := json.Marshal(map[string]string{"command": command, "network": "on"})
	if err != nil {
		t.Fatal(err)
	}
	call := agentcore.AgentToolCall{ID: "push", Name: "bash", Arguments: args}
	fallback := &countingClassifier{v: Verdict{Level: Contain, Source: "llm"}}
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: cwd,
		Classifier: Local{Next: fallback}, Sandboxed: func(string) bool { return true }})
	decision := gate(context.Background(), call)
	if decision == nil || decision.Block || decision.Sandbox || !decision.Direct || !decision.Network || decision.ExecutionBinding == "" {
		var detail string
		if decision != nil && decision.Content != nil {
			detail = agentcore.ContentToText(*decision.Content)
		}
		t.Fatalf("first push decision = %+v detail=%s fallback=%d", decision, detail, fallback.calls)
	}
	if fallback.calls != 0 {
		t.Fatalf("ordinary bound push was re-reviewed %d times", fallback.calls)
	}
	if err := VerifyExecutionBinding(context.Background(), "bash", args, cwd, decision.ExecutionBinding); err != nil {
		t.Fatalf("unchanged HTTPS binding: %v", err)
	}
	gitFactCommand(t, repo, "remote", "set-url", "origin", "https://other.example.com/team/project.git")
	if err := VerifyExecutionBinding(context.Background(), "bash", args, cwd, decision.ExecutionBinding); err == nil {
		t.Fatal("changed HTTPS remote retained authorization")
	}
}

func TestHTTPSBoundPushRejectsChangedHook(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://example.com/team/project.git")
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"command":"git push origin main"}`)
	facts := resolveExecutionFacts(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: args}, repo)
	if !facts.requiresUnconfined || facts.binding == "" {
		t.Fatalf("HTTPS facts = %+v", facts)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutionBinding(context.Background(), "bash", args, repo, facts.binding); err == nil {
		t.Fatal("changed HTTPS pre-push hook retained authorization")
	}
}

func TestHTTPSPushWithExternalHookDoesNotGetDirectEnvelope(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://example.com/team/project.git")
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "pre-push"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitFactCommand(t, repo, "config", "core.hooksPath", external)
	facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery)
	if facts.requiresUnconfined || facts.binding != "" || len(facts.Public) != 1 || facts.Public[0].Resolution != "opaque" {
		t.Fatalf("unbound hook was promoted to a direct envelope: %+v", facts)
	}
}

func TestMixedPushDestinationsDoNotGetDirectEnvelope(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://example.com/team/project.git")
	gitFactCommand(t, repo, "remote", "set-url", "--add", "--push", "origin", "https://example.com/team/project.git")
	gitFactCommand(t, repo, "remote", "set-url", "--add", "--push", "origin", "ext::opaque-helper")
	facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery)
	if facts.requiresUnconfined || facts.binding != "" {
		t.Fatalf("mixed opaque destinations were promoted to a direct envelope: %+v", facts)
	}
}

func TestBoundGitPushDestructiveFormsStillReachReviewer(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://example.com/team/project.git")
	for _, command := range []string{
		"git push --force origin main", "git push origin :refs/heads/main",
		"git push origin +main:main", "git push --delete origin main",
	} {
		t.Run(command, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"command": command, "network": "on"})
			call := agentcore.AgentToolCall{Name: "bash", Arguments: args}
			ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))
			fallback := &countingClassifier{v: Verdict{Level: Contain, Source: "llm"}}
			v := Local{Next: fallback}.Classify(ctx, call.Name, args)
			if fallback.calls != 1 || v.Level != Contain {
				t.Fatalf("dangerous push skipped review: calls=%d verdict=%+v", fallback.calls, v)
			}
		})
	}
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
	if !hasRationale(notes, "Git push") {
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
