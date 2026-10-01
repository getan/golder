package judge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
