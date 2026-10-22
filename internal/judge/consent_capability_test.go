package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// A contained call is still allowed the capabilities the reviewer granted
// separately. Network inside the sandbox is one of them: dropping it here made
// the bash tool refuse a call the reviewer had already authorized.
func TestConsentAutoContainCarriesTheReviewerNetworkGrant(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Network: true, Reasons: []string{"keep it contained, network is fine"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "contain-network", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the contained call should run, got %+v", dec)
	}
	if !dec.Sandbox {
		t.Errorf("containment was lost: %+v", dec)
	}
	if !dec.Network {
		t.Errorf("the reviewer's network grant was lost: %+v", dec)
	}
}

// A reviewer that answers direct:"allow" but grades the call contain does not
// get a contradiction in the transcript: containment is final, and the decision
// line says the escape was refused instead of letting the rationale promise it.
func TestConsentAutoContainRefusesTheEscape(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{
			Level: Contain, Direct: true, Source: "llm",
			Reasons: []string{"递归删除难以恢复，可允许单次越过沙箱清理。"},
		}},
		Cwd:       t.TempDir(),
		Sandboxed: func(string) bool { return true },
		Notify:    func(n Note) { notes = append(notes, n) },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "contain-escape", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the contained call should run, got %+v", dec)
	}
	if !dec.Sandbox || dec.Direct {
		t.Errorf("envelope = %+v, want containment and no escape", dec)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want one sandbox note", notes)
	}
	got := FormatNote(notes[0])
	if !strings.Contains(got, "contained · the sandbox escape was not granted") {
		t.Errorf("note = %q, want the refused escape in the decision line", got)
	}
}

// Auto's allow/confirm authorizes the command, not a sandbox escape the
// reviewer never granted. A refused escape blocks the call — running it bare
// because the level happened to be confirm is how a direct:deny became an
// escape.
func TestConsentAutoDeniedEscapeIsBlocked(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{
			Level: Confirm, Direct: false, Source: "llm",
			Reasons: []string{"命令本身可以运行，但沙箱外请求没有获准。"},
		}},
		Cwd:       t.TempDir(),
		Sandboxed: func(string) bool { return true },
		Notify:    func(n Note) { notes = append(notes, n) },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "deny-escape", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || !dec.Block {
		t.Fatalf("a refused escape must block, got %+v", dec)
	}
	if dec.Direct {
		t.Errorf("a refused escape ran unconfined: %+v", dec)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want one denial", notes)
	}
	if !notes[0].Escalation {
		t.Errorf("note = %+v, want the refused escape marked", notes[0])
	}
	got := FormatNote(notes[0])
	if !strings.Contains(got, "denied · the sandbox escape was not granted") {
		t.Errorf("note = %q, want the refused escape in the decision line", got)
	}
}

// An escape the reviewer did grant is announced as such, not as an ordinary
// approval: the user must be able to see that the call left the sandbox.
func TestConsentAutoApprovedEscapeIsAnnounced(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{
			Level: Confirm, Direct: true, Source: "llm",
			Reasons: []string{"本次任务创建的临时截图目录，清理范围明确。"},
		}},
		Cwd:       t.TempDir(),
		Sandboxed: func(string) bool { return true },
		Notify:    func(n Note) { notes = append(notes, n) },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "allow-escape", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || dec.Block || !dec.Direct {
		t.Fatalf("the granted escape should run unconfined, got %+v", dec)
	}
	if len(notes) != 1 || notes[0].Kind != NoteEscaped {
		t.Fatalf("notes = %+v, want one NoteEscaped", notes)
	}
	got := FormatNote(notes[0])
	if !strings.Contains(got, "runs OUTSIDE the sandbox") {
		t.Errorf("note = %q, want the escape announced", got)
	}
}

// Enforce mode is the strictest setting: every bash call is contained, so a
// Direct grant cannot be honored. Saying "runs OUTSIDE" while the tool forces a
// sandbox is a lie about the one outcome that must never be misreported.
func TestConsentAutoEnforceContainsTheEscape(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Enforce:    true,
		Classifier: consentClassifier{v: Verdict{Level: Allow, Direct: true, Source: "llm", Reasons: []string{"用户要求清理"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Notify:     func(n Note) { notes = append(notes, n) },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "enforce-escape", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the contained call should run, got %+v", dec)
	}
	if !dec.Sandbox || dec.Direct {
		t.Errorf("envelope = %+v, want enforce containment and no escape", dec)
	}
	if len(notes) != 1 || notes[0].Kind != NoteSandboxed || !notes[0].Escalation {
		t.Fatalf("notes = %+v, want one contained-escape note", notes)
	}
	got := FormatNote(notes[0])
	if !strings.Contains(got, "contained · the sandbox escape was not granted") {
		t.Errorf("note = %q, want the refused escape named", got)
	}
	if strings.Contains(got, "runs OUTSIDE") {
		t.Errorf("note = %q, want enforcement reflected", got)
	}
}

// Ask cannot offer an escape enforcement will not perform. The need is omitted,
// and a stale unconfined answer still lands contained.
func TestConsentAskEnforceDoesNotOfferEscape(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Enforce:    true,
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Source: "llm", Reasons: []string{"需要确认"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Notify:     func(n Note) { notes = append(notes, n) },
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			if hasNeed(req.Needs, CapUnconfined) {
				t.Fatalf("enforce offered an escape: %+v", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "enforce-ask", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the approved call should run, got %+v", dec)
	}
	if !dec.Sandbox || dec.Direct {
		t.Errorf("envelope = %+v, want enforce containment and no escape", dec)
	}
	if len(notes) != 1 || notes[0].Kind != NoteSandboxed {
		t.Fatalf("notes = %+v, want one contained note", notes)
	}
}

// RequireSandbox is the default, not a veto: an explicit one-call Direct is
// what the tool's own route honors before it checks for a runner. Enforce is
// the exception; it stays fail-closed.
func TestConsentAutoDirectOverridesRequireSandboxWithoutRunner(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:           permissions.New(permissions.Auto),
		RequireSandbox: true,
		Sandboxed:      func(string) bool { return false },
		Classifier:     consentClassifier{v: Verdict{Level: Allow, Direct: true, Source: "llm", Reasons: []string{"用户要求这个临时目录清理"}}},
		Cwd:            t.TempDir(),
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "direct-no-runner", Name: "bash",
		Arguments: json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("an explicit Direct should override the missing runner, got %+v", dec)
	}
	if !dec.Direct {
		t.Errorf("decision = %+v, want the escape granted", dec)
	}
}

// Without a runner and without a Direct, a bash call cannot be contained at
// all. The failure names that, not a refused escape the call never requested.
func TestConsentAutoNoRunnerWithoutEscapeBlocks(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:           permissions.New(permissions.Auto),
		RequireSandbox: true,
		Sandboxed:      func(string) bool { return false },
		Classifier:     consentClassifier{v: Verdict{Level: Allow, Source: "llm", Reasons: []string{"read only"}}},
		Cwd:            t.TempDir(),
		Notify:         func(n Note) { notes = append(notes, n) },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "no-runner", Name: "bash",
		Arguments: json.RawMessage(`{"command":"ls"}`)})
	if dec == nil || !dec.Block {
		t.Fatalf("a call that cannot be contained must block, got %+v", dec)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want one denial", notes)
	}
	if got := FormatNote(notes[0]); !strings.Contains(got, "no OS sandbox") {
		t.Errorf("note = %q, want the missing runner named", got)
	}
}

// Only bash has an escape request. A native tool that happens to carry the
// sandbox_permissions argument must not inherit the bash-only Direct bit.
func TestConsentAutoNativeToolCannotEscape(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Allow, Direct: true, Source: "llm", Reasons: []string{"read only"}}},
		Cwd:        t.TempDir(),
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "native-escape", Name: "read",
		Arguments: json.RawMessage(`{"path":"x","sandbox_permissions":"require_escalated"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the read should run, got %+v", dec)
	}
	if dec.Direct {
		t.Errorf("decision = %+v, want no Direct on a native tool", dec)
	}
}

// Containment must not become a way around the network answer: a reviewer that
// says network:deny leaves a capability need, and the call is blocked rather
// than run into the bash tool's own guard.
func TestConsentAutoContainWithoutNetworkBlocks(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Reasons: []string{"contain it, no network"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "contain-no-network", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || !dec.Block {
		t.Fatalf("contain without a network grant must block, got %+v", dec)
	}
}

// In ask mode the person is the authority. If they answer the escalation
// request with "run it outside the sandbox", the reviewer's contain grade must
// not override that answer; both bits on the wire would make the executor
// ignore the escape and run contained.
func TestConsentAskUnconfinedAnswerWinsOverContain(t *testing.T) {
	grantstest.Install(t)
	var notes []Note
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Reasons: []string{"containable"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Notify:     func(n Note) { notes = append(notes, n) },
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			if !hasNeed(req.Needs, CapUnconfined) {
				t.Fatalf("the escalation was not offered as a capability: %+v", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "ask-unconfined", Name: "bash",
		Arguments: json.RawMessage(`{"command":"cat /tmp/contained-escape","sandbox_permissions":"require_escalated","justification":"the path is outside"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the approved call should run, got %+v", dec)
	}
	if !dec.Direct {
		t.Errorf("the person's unconfined answer was dropped: %+v", dec)
	}
	if dec.Sandbox {
		t.Errorf("the person's unconfined answer was overridden by containment: %+v", dec)
	}
	if len(notes) != 1 || notes[0].Kind != NoteEscaped {
		t.Errorf("notes = %+v, want the unconfined answer announced as an escape", notes)
	}
}

// An approval grants the Needs set it was shown, not an inference. A driver
// that returns Allow without the network need must not make the gate invent a
// network grant the person never approved.
func TestConsentAskDoesNotInferNetworkFromApproval(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Reasons: []string{"needs a decision"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "ask-network-subset", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the approved call should still run, got %+v", dec)
	}
	if dec.Network {
		t.Errorf("network was granted although the answer omitted it: %+v", dec)
	}
}

// ChainGates owns the join between two gates, not just their blocks. A Bare
// envelope from the first gate must survive a second gate that adds another
// capability; before this it was silently dropped and the call re-contained.
func TestChainGatesKeepsBareEnvelope(t *testing.T) {
	first := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Bare: true, BareReason: "the command is readable"}
	}
	second := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	dec := ChainGates(first, second)(context.Background(), agentcore.AgentToolCall{Name: "bash"})
	if dec == nil || !dec.Bare || dec.BareReason != "the command is readable" {
		t.Fatalf("the first gate's Bare envelope was lost: %+v", dec)
	}
	if !dec.Network {
		t.Errorf("the second gate's network grant was lost: %+v", dec)
	}
}
