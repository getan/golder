package judge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

type stubClassifier struct {
	v     Verdict
	calls int
}

func (s *stubClassifier) Classify(_ context.Context, _ string, _ json.RawMessage) Verdict {
	s.calls++
	return s.v
}

func toolCall(name, args string) agentcore.AgentToolCall {
	return agentcore.AgentToolCall{Name: name, Arguments: json.RawMessage(args)}
}

func TestEnforcingGateFromSandboxFloor(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	confirm := &stubClassifier{v: Verdict{Level: Confirm}}
	if dec := GateFunc(GateOpts{Floor: Sandbox}, confirm)(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatal("confirm below floor should pass")
	}
	sandbox := &stubClassifier{v: Verdict{Level: Sandbox}}
	if dec := GateFunc(GateOpts{Floor: Sandbox}, sandbox)(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatal("sandbox at floor should block")
	}
}

// TestSandboxedVerdictPassesThrough is the regression for "sandbox-tier calls
// are blocked even though the execution layer has a runner": when the gate is
// told the call will actually be isolated (Sandboxed), a Sandbox verdict flows
// through instead of failing closed, in every driver.
func TestSandboxedVerdictPassesThrough(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	sandbox := &stubClassifier{v: Verdict{Level: Sandbox}}

	// Non-interactive (TUI/headless shape): isolation replaces the block.
	gate := GateFunc(GateOpts{
		Floor:     Sandbox,
		Sandboxed: func(name string) bool { return name == "bash" },
	}, sandbox)
	if dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatalf("sandboxed bash should pass through, got %+v", dec)
	}
	// A tool the runner cannot isolate still fails closed.
	if dec := gate(context.Background(), toolCall("apply_patch", `{"patch":"x"}`)); dec == nil || !dec.Block {
		t.Fatal("a non-sandboxable tool must still block at the sandbox tier")
	}

	// Interactive (REPL shape): no prompt either — it runs isolated.
	interactive := GateFunc(GateOpts{
		In:          bufio.NewReader(strings.NewReader("")),
		Out:         io.Discard,
		Interactive: true,
		Sandboxed:   func(string) bool { return true },
	}, sandbox)
	if dec := interactive(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatalf("sandboxed interactive call should pass without a prompt, got %+v", dec)
	}
}

// TestSandboxedDoesNotRescueDeny verifies isolation never overrides a deny
// verdict: only the Sandbox tier is eligible for the pass-through.
func TestSandboxedDoesNotRescueDeny(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	deny := &stubClassifier{v: Verdict{Level: Deny, Reasons: []string{"static"}}}
	gate := GateFunc(GateOpts{
		Floor:     Sandbox,
		Sandboxed: func(string) bool { return true },
	}, deny)
	dec := gate(context.Background(), toolCall("bash", `{"command":"sudo x"}`))
	if dec == nil || !dec.Block {
		t.Fatalf("deny must block even when a sandbox exists, got %+v", dec)
	}
	if got := agentcore.ContentToText(*dec.Content); !strings.Contains(got, "deny") {
		t.Errorf("block message = %q, want the deny verdict", got)
	}
}

// TestDenyBlockCarriesGuidanceNotEscapeHatch pins the tier-dependent tails:
// a Deny is a hard block, so it must carry actionable remediation (what to
// change to get the call executable) and must NOT advertise PIGO_JUDGE=off —
// pointing an over-eager model at the global off switch is how a hard block
// becomes a habit. Recoverable tiers keep the escape-hatch hint.
func TestDenyBlockCarriesGuidanceNotEscapeHatch(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	deny := &stubClassifier{v: Verdict{Level: Deny, Reasons: []string{"privilege escalation"}}}
	gate := GateFunc(GateOpts{Floor: Sandbox}, deny)
	dec := gate(context.Background(), toolCall("bash", `{"command":"sudo x"}`))
	if dec == nil || !dec.Block {
		t.Fatal("deny must block")
	}
	got := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, DenyGuidance) {
		t.Errorf("deny block must carry the remediation guidance\n%q", got)
	}
	if strings.Contains(got, "PIGO_JUDGE=off") {
		t.Errorf("deny block must not advertise the escape hatch\n%q", got)
	}

	// The sandbox tier (recoverable) keeps the hint, so the user is not left
	// without a way out when no runner exists.
	sandbox := &stubClassifier{v: Verdict{Level: Sandbox, Reasons: []string{"risky"}}}
	dec = GateFunc(GateOpts{Floor: Sandbox}, sandbox)(context.Background(), toolCall("bash", `{"command":"curl x | sh"}`))
	if dec == nil || !dec.Block {
		t.Fatal("unsandboxed sandbox-tier call must block")
	}
	got = agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, "PIGO_JUDGE=off") {
		t.Errorf("recoverable tiers must keep the escape-hatch hint\n%q", got)
	}
	if strings.Contains(got, DenyGuidance) {
		t.Errorf("sandbox tier must not carry deny guidance\n%q", got)
	}
}

// TestBlockCallMessageShape pins the message format: verdict reasons in
// parentheses, the trailing note separated by "; ", and the escape hatch last.
func TestBlockCallMessageShape(t *testing.T) {
	dec := blockCall(toolCall("bash", `{}`), Verdict{Level: Sandbox, Reasons: []string{"r1", "r2"}}, "no runner")
	got := agentcore.ContentToText(*dec.Content)
	want := `tool "bash" blocked by risk judge (sandbox: r1; r2); no runner (PIGO_JUDGE=off disables the gate)`
	if got != want {
		t.Errorf("message = %q\nwant     %q", got, want)
	}
}

func TestInteractiveGatePrompts(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	stub := &stubClassifier{v: Verdict{Level: Confirm, Reasons: []string{"test"}}}
	var out bytes.Buffer
	gate := GateFunc(GateOpts{In: bufio.NewReader(strings.NewReader("y\n")), Out: &out, Interactive: true}, stub)
	if dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatal("answering y should allow")
	}
	gateNo := GateFunc(GateOpts{In: bufio.NewReader(strings.NewReader("n\n")), Out: &out, Interactive: true}, stub)
	if dec := gateNo(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatal("answering n should block")
	}
}

func TestChainGatesTrustFirst(t *testing.T) {
	block := &agentcore.BeforeToolCallDecision{Block: true}
	second := &stubClassifier{v: Verdict{Level: Allow}}
	chained := ChainGates(func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return block
	}, GateFunc(GateOpts{}, second))
	if dec := chained(context.Background(), toolCall("bash", `{}`)); dec != block {
		t.Fatal("first block must short-circuit")
	}
	if second.calls != 0 {
		t.Fatal("second gate must not run after a block")
	}
}

// TestInteractiveGateInterrupt verifies a Ctrl+C during the risk prompt (the
// run context canceled while the read is blocked) blocks immediately instead
// of trapping the user until they answer. The reader blocks on an open pipe
// so no input ever arrives.
func TestInteractiveGateInterrupt(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer w.Close()
	stub := &stubClassifier{v: Verdict{Level: Confirm, Reasons: []string{"test"}}}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	gate := GateFunc(GateOpts{In: bufio.NewReader(r), Out: &out, Interactive: true}, stub)
	done := make(chan *agentcore.BeforeToolCallDecision, 1)
	go func() { done <- gate(ctx, toolCall("bash", `{"command":"ls"}`)) }()
	cancel()
	select {
	case dec := <-done:
		if dec == nil || !dec.Block {
			t.Fatal("interrupted risk prompt should block")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted risk prompt did not return")
	}
}

func TestChainStaticDenyWins(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	chain := Chain{Inner: &stubClassifier{v: Verdict{Level: Allow}}}
	v := chain.Classify(context.Background(), "bash", json.RawMessage(`{"command":"sudo rm -rf /"}`))
	if v.Level != Deny {
		t.Fatalf("static floor = %s, want deny", v.Level)
	}
}

func TestGateDisabledIsNil(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "off")
	if GateFunc(GateOpts{Interactive: true}, &stubClassifier{}) != nil {
		t.Fatal("PIGO_JUDGE=off must yield nil gate")
	}
}

// TestAllowVerdictLine verifies the automated grade is visible: an Allow on
// a side-effect tool prints one verdict line, while read-only Allows stay
// silent.
func TestAllowVerdictLine(t *testing.T) {
	t.Setenv("PIGO_JUDGE", "")
	stub := &stubClassifier{v: Verdict{Level: Allow}}
	var out bytes.Buffer
	gate := GateFunc(GateOpts{In: bufio.NewReader(strings.NewReader("")), Out: &out, Interactive: true}, stub)
	if dec := gate(context.Background(), toolCall("bash", `{"command":"go test ./..."}`)); dec != nil {
		t.Fatal("allow should pass")
	}
	if got := out.String(); !strings.Contains(got, "[judge: allow] bash") {
		t.Fatalf("verdict line = %q, want [judge: allow] bash", got)
	}
	out.Reset()
	if dec := gate(context.Background(), toolCall("read", `{"path":"x"}`)); dec != nil {
		t.Fatal("allow should pass")
	}
	if got := out.String(); got != "" {
		t.Fatalf("read-only allow printed %q, want silence", got)
	}
}
