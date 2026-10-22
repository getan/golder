package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

type consentClassifier struct {
	v Verdict
}

func (c consentClassifier) Classify(context.Context, string, json.RawMessage) Verdict { return c.v }

func consentCall(command string) agentcore.AgentToolCall {
	return agentcore.AgentToolCall{ID: "consent-1", Name: "bash",
		Arguments: json.RawMessage(`{"command":` + jsonString(command) + `,"network":"on","sandbox_permissions":"require_escalated"}`)}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// consentOutside is outside the workspace AND outside the system/temp
// whitelist. t.TempDir() cannot serve: the test process's TMPDIR is itself an
// always-readable system root, so a path under it is deliberately silent.
func consentOutside(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatal("no home directory")
	}
	return filepath.Join(home, "golder-consent-test", filepath.Base(t.TempDir()), "outside.txt")
}

// Auto is the promise: once the reviewer says allow, every capability the call
// asked for rides that one verdict. No boundary, network or sandbox prompt.
func TestConsentAutoAllowGrantsEveryNeedWithoutPrompt(t *testing.T) {
	grantstest.Install(t)
	workspace := t.TempDir()
	outside := consentOutside(t)
	asked := 0
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto),
		// The call asks for the network and the sandbox escape; auto's allow is
		// the whole authorization.
		Classifier: consentClassifier{v: Verdict{Level: Allow, Reasons: []string{"safe"}}},
		Cwd:        workspace,
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			asked++
			return ConsentAnswer{}
		},
	})
	dec := gate(context.Background(), consentCall("cat "+outside+" && curl https://example.com"))
	if dec == nil || dec.Block {
		t.Fatalf("auto allow must run, got %+v", dec)
	}
	if asked != 0 {
		t.Fatalf("auto allow must not prompt, asked %d", asked)
	}
	if !dec.Network || !dec.Direct {
		t.Errorf("decision lost a capability: %+v", dec)
	}
	if dec.ReadGrant != outside {
		t.Errorf("read grant = %q, want %q", dec.ReadGrant, outside)
	}
}

// Ask is one question for the whole call: the same three needs arrive in one
// request instead of three dialogs.
func TestConsentAskCombinesNeedsIntoOnePrompt(t *testing.T) {
	grantstest.Install(t)
	workspace := t.TempDir()
	outside := consentOutside(t)
	asked := 0
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Source: LocalSource, Reasons: []string{"needs a decision"}}},
		Cwd:        workspace,
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			asked++
			if req.Source != LocalSource {
				t.Fatalf("consent source = %q, want %q", req.Source, LocalSource)
			}
			if len(req.Needs) != 3 {
				t.Fatalf("one prompt should carry all needs, got %+v", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
		},
	})
	dec := gate(context.Background(), consentCall("cat "+outside+" && curl https://example.com"))
	if dec == nil || dec.Block {
		t.Fatalf("approved call must run, got %+v", dec)
	}
	if asked != 1 {
		t.Fatalf("prompts = %d, want exactly one", asked)
	}
	if !dec.Network || !dec.Direct || dec.ReadGrant != outside {
		t.Errorf("answer did not apply every need: %+v", dec)
	}
	if !grants.Allows(outside, grants.Read) {
		t.Error("the one-call read answer was not recorded in the authorization table")
	}
}

// A safe local/JEV decision with no expansion is the fast path: ask mode does
// not interrupt for it.
func TestConsentAskSafeCallDoesNotPrompt(t *testing.T) {
	grantstest.Install(t)
	asked := 0
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Allow, Source: LocalSource, Reasons: []string{"routine"}}},
		Cwd:        t.TempDir(),
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			asked++
			return ConsentAnswer{}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "safe", Name: "bash",
		Arguments: json.RawMessage(`{"command":"echo hi"}`)})
	if asked != 0 {
		t.Fatalf("safe local call prompted %d times", asked)
	}
	if dec != nil && dec.Block {
		t.Fatalf("safe local call blocked: %+v", dec)
	}
}

// Full access is exactly the static floor: no boundary question, no network
// question, no dialog.
func TestConsentFullAccessOnlyStaticFloor(t *testing.T) {
	grantstest.Install(t)
	workspace := t.TempDir()
	outside := consentOutside(t)
	asked := 0
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.FullAccess),
		Cwd:  workspace,
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			asked++
			return ConsentAnswer{}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "full", Name: "read",
		Arguments: json.RawMessage(`{"path":` + jsonString(outside) + `}`)})
	if asked != 0 {
		t.Fatalf("full access prompted %d times", asked)
	}
	if dec == nil || dec.Block || dec.ReadGrant != outside {
		t.Fatalf("full access should grant the path without a dialog, got %+v", dec)
	}
	if deny := gate(context.Background(), consentCall("sudo rm -rf /")); deny == nil || !deny.Block {
		t.Fatalf("full access must keep the static floor, got %+v", deny)
	}
}

func TestConsentReadOnlyRejectsExpansion(t *testing.T) {
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.ReadOnly),
		Classifier: consentClassifier{v: Verdict{Level: Allow}},
		Cwd:        t.TempDir(),
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("read-only must not ask")
			return ConsentAnswer{}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "ro", Name: "read",
		Arguments: json.RawMessage(`{"path":` + jsonString(consentOutside(t)) + `}`)})
	if dec == nil || !dec.Block {
		t.Fatalf("read-only outside read should block, got %+v", dec)
	}
}

func TestConsentAutoDenyDoesNotPrompt(t *testing.T) {
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Deny, Reasons: []string{"no"}}},
		Cwd:        t.TempDir(),
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("auto deny must not prompt")
			return ConsentAnswer{}
		},
	})
	if dec := gate(context.Background(), consentCall("echo hi")); dec == nil || !dec.Block {
		t.Fatalf("deny should block, got %+v", dec)
	}
}

func recoveryRequest(path string) ConsentRequest {
	return ConsentRequest{
		Tool: "bash", Summary: "cat " + path,
		Rationale: "the sandbox denied " + path,
		Needs:     []Need{{Cap: CapReadOutside, Path: path, Dir: path}},
	}
}

// Auto recovery is reviewer-only: it must never call the human Confirm channel,
// whether the reviewer allows or refuses the denied path.
func TestReviewRecoveryAutoNeverAsksAPerson(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Verdict
		want bool
	}{
		{"allow", Verdict{Level: Allow, Reasons: []string{"the retry is safe"}}, true},
		{"deny", Verdict{Level: Deny, Reasons: []string{"the path is not needed"}}, false},
		{"contain", Verdict{Level: Contain, Reasons: []string{"keep it contained"}}, false},
		{"failure", Verdict{Failed: true, Reasons: []string{"review unavailable"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := 0
			ans := ReviewRecovery(context.Background(), ConsentOpts{
				Mode:       permissions.New(permissions.Auto),
				Classifier: consentClassifier{v: tc.v},
				Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
					asked++
					return ConsentAnswer{}
				},
			}, recoveryRequest("/outside/file"))
			if asked != 0 {
				t.Fatalf("auto recovery asked a person %d times", asked)
			}
			if ans.Allow != tc.want {
				t.Fatalf("allow = %v, want %v (reason %q)", ans.Allow, tc.want, ans.Reason)
			}
			if tc.want && (ans.Scope != ScopeOnce || len(ans.Needs) != 1) {
				t.Errorf("allow answer = %+v, want one once-only need", ans)
			}
		})
	}
}

// Ask is the one human shape: the reviewer filters a hard denial, otherwise
// the same single Need list is put to the person once.
func TestReviewRecoveryAskOnePrompt(t *testing.T) {
	asked := 0
	ans := ReviewRecovery(context.Background(), ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Reasons: []string{"needs a decision"}}},
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			asked++
			if len(req.Needs) != 1 || req.Needs[0].Path != "/outside/file" {
				t.Fatalf("prompt needs = %+v", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeSession, Needs: req.Needs}
		},
	}, recoveryRequest("/outside/file"))
	if asked != 1 || !ans.Allow || ans.Scope != ScopeSession {
		t.Fatalf("ask recovery = %+v, prompts=%d", ans, asked)
	}
}

func TestReviewRecoveryAskReviewerDenyDoesNotPrompt(t *testing.T) {
	asked := 0
	ans := ReviewRecovery(context.Background(), ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Deny, Reasons: []string{"no"}}},
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			asked++
			return ConsentAnswer{}
		},
	}, recoveryRequest("/outside/file"))
	if asked != 0 || ans.Allow {
		t.Fatalf("denied recovery = %+v, prompts=%d", ans, asked)
	}
}

func TestReviewRecoveryPolicyEdges(t *testing.T) {
	path := "/outside/file"
	if ans := ReviewRecovery(context.Background(), ConsentOpts{Mode: permissions.New(permissions.FullAccess)}, recoveryRequest(path)); !ans.Allow || ans.Scope != ScopeOnce {
		t.Errorf("full-access recovery = %+v, want once allow", ans)
	}
	if ans := ReviewRecovery(context.Background(), ConsentOpts{Mode: permissions.New(permissions.ReadOnly)}, recoveryRequest(path)); ans.Allow {
		t.Errorf("read-only recovery = %+v, want deny", ans)
	}
}
