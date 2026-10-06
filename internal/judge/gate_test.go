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

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
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

// noteRecorder captures the notes a gate emits.
type noteRecorder struct{ notes []Note }

func (r *noteRecorder) emit(n Note) { r.notes = append(r.notes, n) }

func TestPermissionGateAutoApprovesConfirm(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Confirm, Risk: "medium", Authorization: "high", Reasons: []string{"recoverable install"}}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{Classifier: stub, Notify: rec.emit})
	if dec := gate(context.Background(), toolCall("bash", `{"command":"npm install"}`)); dec != nil {
		t.Fatalf("auto mode should approve a confirm verdict, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteApproved {
		t.Fatalf("notes = %+v, want one NoteApproved", rec.notes)
	}
	if rec.notes[0].Rationale != "recoverable install" {
		t.Errorf("rationale = %q", rec.notes[0].Rationale)
	}
}

func TestPermissionGateAutoSandboxRoutes(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Sandbox, Reasons: []string{"pipes remote content into a shell"}}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: stub,
		Sandboxed:  func(name string) bool { return name == "bash" },
		Notify:     rec.emit,
	})
	dec := gate(context.Background(), toolCall("bash", `{"command":"curl x | sh"}`))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("sandboxable verdict must pass carrying a sandbox request, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteSandboxed {
		t.Fatalf("notes = %+v, want one NoteSandboxed", rec.notes)
	}
	// A tool the runner cannot isolate has no safe path: blocked, noted.
	rec.notes = nil
	if dec := gate(context.Background(), toolCall("apply_patch", `{"patch":"x"}`)); dec == nil || !dec.Block {
		t.Fatal("a non-sandboxable sandbox verdict must block")
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteBlockedNoPrompt {
		t.Fatalf("notes = %+v, want one NoteBlockedNoPrompt", rec.notes)
	}
}

func TestPermissionGateAutoDeniesWithGuidance(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Deny, Reasons: []string{"privilege escalation"}}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{Classifier: stub, Notify: rec.emit})
	dec := gate(context.Background(), toolCall("bash", `{"command":"sudo x"}`))
	if dec == nil || !dec.Block {
		t.Fatal("deny must block")
	}
	got := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, DenyGuidance) {
		t.Errorf("deny block must carry the remediation guidance\n%q", got)
	}
	if strings.Contains(got, "GOLDER_JUDGE") {
		t.Errorf("block must not advertise a removed escape hatch\n%q", got)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteDenied {
		t.Fatalf("notes = %+v, want one NoteDenied", rec.notes)
	}
}

func TestPermissionGateAskPrompts(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Confirm, Reasons: []string{"test"}}}
	var out bytes.Buffer
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Ask), GateOpts{
		In:         bufio.NewReader(strings.NewReader("y\n")),
		Out:        &out,
		Classifier: stub,
		Notify:     rec.emit,
	})
	if dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec != nil {
		t.Fatal("answering y should allow")
	}
	if len(rec.notes) != 0 {
		t.Fatalf("a prompted decision must not also emit a note, got %+v", rec.notes)
	}
	var out2 bytes.Buffer
	gateNo := PermissionGate(permissions.New(permissions.Ask), GateOpts{
		In:         bufio.NewReader(strings.NewReader("n\n")),
		Out:        &out2,
		Classifier: stub,
	})
	if dec := gateNo(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatal("answering n should block")
	}
}

func TestPermissionGateAskNoPromptBlocks(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Confirm, Reasons: []string{"state changing"}}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Ask), GateOpts{Classifier: stub, Notify: rec.emit})
	dec := gate(context.Background(), toolCall("bash", `{"command":"npm install"}`))
	if dec == nil || !dec.Block {
		t.Fatal("ask mode without a prompt must fail closed")
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteBlockedNoPrompt {
		t.Fatalf("notes = %+v, want one NoteBlockedNoPrompt", rec.notes)
	}
}

func TestPermissionGateReadOnlyBlocksMutating(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Allow}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.ReadOnly), GateOpts{Classifier: stub, Notify: rec.emit})
	if dec := gate(context.Background(), toolCall("read", `{"path":"x"}`)); dec != nil {
		t.Fatal("read is allowed in read-only mode")
	}
	if dec := gate(context.Background(), toolCall("grep", `{"pattern":"x"}`)); dec != nil {
		t.Fatal("grep is allowed in read-only mode")
	}
	dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`))
	if dec == nil || !dec.Block {
		t.Fatal("bash must be blocked in read-only mode")
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteReadOnly {
		t.Fatalf("notes = %+v, want one NoteReadOnly", rec.notes)
	}
	if stub.calls != 0 {
		t.Fatal("read-only blocks must not spend a reviewer call")
	}
}

func TestPermissionGateFullAccessKeepsStaticFloor(t *testing.T) {
	stub := &stubClassifier{v: Verdict{Level: Confirm}}
	gate := PermissionGate(permissions.New(permissions.FullAccess), GateOpts{Classifier: stub})
	if dec := gate(context.Background(), toolCall("bash", `{"command":"echo hi"}`)); dec != nil {
		t.Fatal("full-access must not gate ordinary calls")
	}
	if dec := gate(context.Background(), toolCall("bash", `{"command":"sudo rm -rf /"}`)); dec == nil || !dec.Block {
		t.Fatal("full-access must keep the static hard-deny floor")
	}
	if stub.calls != 0 {
		t.Fatal("full-access must not spend a reviewer call")
	}
}

func TestPermissionGateReviewerFailure(t *testing.T) {
	failed := &stubClassifier{v: Verdict{Level: Confirm, Failed: true, Reasons: []string{"timeout"}}}
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: failed,
		Sandboxed:  func(name string) bool { return name == "bash" },
		Notify:     rec.emit,
	})
	dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("a failed review on a sandboxable tool must run contained, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteUnavailable {
		t.Fatalf("notes = %+v, want one NoteUnavailable", rec.notes)
	}
	// No sandbox for this tool: the failure must not become an allow.
	if dec := gate(context.Background(), toolCall("apply_patch", `{"patch":"x"}`)); dec == nil || !dec.Block {
		t.Fatal("a failed review without a sandbox must fail closed")
	}
}

func TestPermissionGateNilClassifierFailsClosed(t *testing.T) {
	gate := PermissionGate(permissions.New(permissions.Auto), GateOpts{})
	if dec := gate(context.Background(), toolCall("bash", `{"command":"ls"}`)); dec == nil || !dec.Block {
		t.Fatal("no reviewer must fail closed for gated tools")
	}
	if dec := gate(context.Background(), toolCall("read", `{"path":"x"}`)); dec != nil {
		t.Fatal("read-only tools pass without a reviewer")
	}
}

func TestChainGatesTrustFirst(t *testing.T) {
	block := &agentcore.BeforeToolCallDecision{Block: true}
	second := &stubClassifier{v: Verdict{Level: Allow}}
	chained := ChainGates(func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return block
	}, PermissionGate(permissions.New(permissions.Auto), GateOpts{Classifier: second}))
	if dec := chained(context.Background(), toolCall("bash", `{}`)); dec != block {
		t.Fatal("first block must short-circuit")
	}
	if second.calls != 0 {
		t.Fatal("second gate must not run after a block")
	}
}

// TestChainGatesPreservesSandboxRequest verifies a sandbox request from the
// first gate survives a later no-op gate: the TUI chains the permission gate
// before the remote-confirm seam, and the seam returning nil must not drop the
// execution-layer containment decision.
func TestChainGatesPreservesSandboxRequest(t *testing.T) {
	first := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Sandbox: true}
	}
	chained := ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return nil
	})
	dec := chained(context.Background(), toolCall("bash", `{"command":"ls"}`))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("sandbox request must survive composition, got %+v", dec)
	}

	// A later gate that rewrites arguments keeps the sandbox request too.
	chained = ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{UpdatedInput: json.RawMessage(`{"command":"echo hi"}`)}
	})
	dec = chained(context.Background(), toolCall("bash", `{"command":"ls"}`))
	if dec == nil || !dec.Sandbox || len(dec.UpdatedInput) == 0 {
		t.Fatalf("sandbox request must merge with a later rewrite, got %+v", dec)
	}

	// A later block still wins: containment is moot when the call is refused.
	chained = ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Block: true}
	})
	if dec := chained(context.Background(), toolCall("bash", `{}`)); dec == nil || !dec.Block {
		t.Fatal("a later block must win over a sandbox request")
	}
}

// TestPermissionGateInterrupt verifies a Ctrl+C during the risk prompt (the
// run context canceled while the read is blocked) blocks immediately instead
// of trapping the user until they answer. The reader blocks on an open pipe
// so no input ever arrives.
func TestPermissionGateInterrupt(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer w.Close()
	stub := &stubClassifier{v: Verdict{Level: Confirm, Reasons: []string{"test"}}}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	gate := PermissionGate(permissions.New(permissions.Ask), GateOpts{In: bufio.NewReader(r), Out: &out, Classifier: stub})
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
	chain := Chain{Inner: &stubClassifier{v: Verdict{Level: Allow}}}
	v := chain.Classify(context.Background(), "bash", json.RawMessage(`{"command":"sudo rm -rf /"}`))
	if v.Level != Deny {
		t.Fatalf("static floor = %s, want deny", v.Level)
	}
}

// TestBlockCallMessageShape pins the model-facing block format: verdict
// reasons in parentheses, the trailing note separated by "; ".
func TestBlockCallMessageShape(t *testing.T) {
	dec := blockCall(toolCall("bash", `{}`), Verdict{Level: Sandbox, Reasons: []string{"r1", "r2"}}, "no runner")
	got := agentcore.ContentToText(*dec.Content)
	want := `tool "bash" blocked by risk judge (sandbox: r1; r2); no runner`
	if got != want {
		t.Errorf("message = %q\nwant     %q", got, want)
	}
}

func TestConversationLanguage(t *testing.T) {
	zh := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("帮我跑一下测试")}},
	}
	if got := ConversationLanguage(zh); got != "zh" {
		t.Errorf("lang = %q, want zh", got)
	}
	en := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("run the tests")}},
	}
	if got := ConversationLanguage(en); got != "en" {
		t.Errorf("lang = %q, want en", got)
	}
	if got := ConversationLanguage(nil); got != "en" {
		t.Errorf("empty lang = %q, want en", got)
	}
}

func TestFormatNoteLocalized(t *testing.T) {
	zh := FormatNote(Note{Tool: "bash", Kind: NoteApproved, Risk: "medium", Authorization: "high", Rationale: "常规操作", Lang: "zh"})
	if !strings.Contains(zh, "自动审批通过") || !strings.Contains(zh, "风险：中") || !strings.Contains(zh, "常规操作") {
		t.Errorf("zh note = %q", zh)
	}
	en := FormatNote(Note{Tool: "bash", Kind: NoteDenied, Risk: "high", Authorization: "low", Rationale: "dangerous", Lang: "en"})
	if !strings.Contains(en, "Auto-review denied") || !strings.Contains(en, "risk: high") {
		t.Errorf("en note = %q", en)
	}
}

const escalationArgs = `{"command":"rm -rf ~/work/harness/scratch","sandbox_permissions":"require_escalated","justification":"cleanup outside the workspace"}`

func escalationGateOpts(rec *noteRecorder) GateOpts {
	return GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Sandbox, Risk: "high", Authorization: "low", Reasons: []string{"recursive delete outside the workspace"}}},
		Sandboxed:  func(name string) bool { return name == "bash" },
		Notify:     rec.emit,
	}
}

// TestPermissionGateEscalationConflictAsksEvenInAuto locks the conflict rule:
// the model asked to run outside the sandbox while the reviewer graded the
// call Sandbox, so the gate must ask the user even in auto mode — silently
// recontaining the call would loop forever.
func TestPermissionGateEscalationConflictAsksEvenInAuto(t *testing.T) {
	rec := &noteRecorder{}
	var gotReq ApprovalRequest
	opts := escalationGateOpts(rec)
	opts.Confirm = func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
		gotReq = req
		return ApprovalAnswer{Approve: true, Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	if dec := gate(context.Background(), toolCall("bash", escalationArgs)); dec != nil {
		t.Fatalf("approved escalation must run directly without a sandbox bit, got %+v", dec)
	}
	if gotReq.Kind != ApprovalEscalation {
		t.Fatalf("kind = %q, want %q", gotReq.Kind, ApprovalEscalation)
	}
	if gotReq.Justification != "cleanup outside the workspace" {
		t.Errorf("justification = %q, want the model's reason", gotReq.Justification)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteApproved {
		t.Fatalf("notes = %+v, want one NoteApproved", rec.notes)
	}
}

// TestPermissionGateEscalationConflictDenyBlocks: refusing the escalation
// blocks the call with the user-denied message; it must not run unisolated
// and must not fall through to the silent sandbox either.
func TestPermissionGateEscalationConflictDenyBlocks(t *testing.T) {
	rec := &noteRecorder{}
	opts := escalationGateOpts(rec)
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || !dec.Block || dec.Sandbox {
		t.Fatalf("denied escalation must block, got %+v", dec)
	}
	if got := agentcore.ContentToText(*dec.Content); !strings.Contains(got, "denied by the user") {
		t.Errorf("block message = %q", got)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteDenied {
		t.Fatalf("notes = %+v, want one NoteDenied", rec.notes)
	}
}

// TestPermissionGateEscalationWritableGrantContains: when the user answers by
// granting the path instead of unisolating, the gate re-contains the call
// (the driver already registered the grant) and the note records the path.
func TestPermissionGateEscalationWritableGrantContains(t *testing.T) {
	const root = "/Volumes/KIOXIA/rust-target"
	rec := &noteRecorder{}
	opts := escalationGateOpts(rec)
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Approve: true, Answered: true, WritableRoot: root}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("a path grant must re-contain the call, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteApproved || !strings.Contains(rec.notes[0].Rationale, root) {
		t.Fatalf("notes = %+v, want one NoteApproved naming the grant", rec.notes)
	}
	// A denial never carries the grant through, even if the driver sets it.
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Answered: true, WritableRoot: root}
	}
	gate = PermissionGate(permissions.New(permissions.Auto), opts)
	dec = gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || !dec.Block {
		t.Fatalf("deny with a path must still block, got %+v", dec)
	}
}

// TestPermissionGateEscalationConflictNoHumanContains: with nobody to ask
// (headless), the conflict falls back to containment — never to running bare.
func TestPermissionGateEscalationConflictNoHumanContains(t *testing.T) {
	rec := &noteRecorder{}
	gate := PermissionGate(permissions.New(permissions.Auto), escalationGateOpts(rec))
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("no approver must fall back to the sandbox, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteSandboxed {
		t.Fatalf("notes = %+v, want one NoteSandboxed", rec.notes)
	}
	// An answered=false dialog (cancelled UI) counts as nobody: contain, not allow.
	rec.notes = nil
	opts := escalationGateOpts(rec)
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{}
	}
	gate = PermissionGate(permissions.New(permissions.Auto), opts)
	dec = gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("an unanswered dialog must fall back to the sandbox, got %+v", dec)
	}
}

// TestPermissionGateEscalationPromptsOnStdinEvenInAuto: the REPL path asks on
// stdin and, on yes, lets the call run unisolated.
func TestPermissionGateEscalationPromptsOnStdinEvenInAuto(t *testing.T) {
	var out bytes.Buffer
	opts := escalationGateOpts(&noteRecorder{})
	opts.In = bufio.NewReader(strings.NewReader("y\n"))
	opts.Out = &out
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	if dec := gate(context.Background(), toolCall("bash", escalationArgs)); dec != nil {
		t.Fatalf("answering y must let the escalation run, got %+v", dec)
	}
	if !strings.Contains(out.String(), "unisolated") {
		t.Errorf("prompt must spell out that approval runs the call unisolated:\n%s", out.String())
	}
	// No answer means denial, and the call is contained rather than allowed.
	var out2 bytes.Buffer
	opts = escalationGateOpts(&noteRecorder{})
	opts.In = bufio.NewReader(strings.NewReader("n\n"))
	opts.Out = &out2
	gate = PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("answering n must fall back to the sandbox, got %+v", dec)
	}
}

// TestEscalationRequested pins the argument parsing: only the exact
// require_escalated value counts, and malformed arguments are never treated
// as a request.
func TestEscalationRequested(t *testing.T) {
	cases := []struct {
		args string
		want bool
	}{
		{`{"command":"x","sandbox_permissions":"require_escalated"}`, true},
		{`{"command":"x","sandbox_permissions":" Require_Escalated "}`, true},
		{`{"command":"x","sandbox_permissions":"use_default"}`, false},
		{`{"command":"x"}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		if got := escalationRequested(toolCall("bash", c.args)); got != c.want {
			t.Errorf("escalationRequested(%s) = %v, want %v", c.args, got, c.want)
		}
	}
}
