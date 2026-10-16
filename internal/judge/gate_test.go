package judge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
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
	if dec := gate(context.Background(), toolCall("bash", `{"command":"go test ./..."}`)); dec != nil {
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
	if dec := gateNo(context.Background(), toolCall("bash", `{"command":"go test ./..."}`)); dec == nil || !dec.Block {
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
	dec := gate(context.Background(), toolCall("bash", `{"command":"go test ./..."}`))
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
	// A read tool fails closed too. It used to pass, because the table said it
	// had no side effects so no reviewer was needed; with every call graded, a
	// missing reviewer is missing for `read` as well. The floor above still
	// decides credential material, so this is the conservative half only.
	if dec := gate(context.Background(), toolCall("read", `{"path":"x"}`)); dec == nil || !dec.Block {
		t.Fatalf("a read with no reviewer must fail closed, got %+v", dec)
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

	// A later non-blocking decision keeps the sandbox request too.
	chained = ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{}
	})
	dec = chained(context.Background(), toolCall("bash", `{"command":"ls"}`))
	if dec == nil || !dec.Sandbox {
		t.Fatalf("sandbox request must survive a later non-blocking decision, got %+v", dec)
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
	// A command the reviewer must grade, so the prompt is actually reached.
	go func() { done <- gate(ctx, toolCall("bash", `{"command":"go test ./..."}`)) }()
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
	zh := FormatNote(Note{Tool: "bash", Mode: "auto", Kind: NoteApproved, Risk: "medium", Authorization: "high", Rationale: "常规操作", Lang: "zh"})
	if !strings.Contains(zh, "bash · auto · allow → 已批准") || !strings.Contains(zh, "风险：中") || !strings.Contains(zh, "常规操作") {
		t.Errorf("zh note = %q", zh)
	}
	en := FormatNote(Note{Tool: "bash", Mode: "ask", Kind: NoteDenied, Level: Deny, Risk: "high", Authorization: "low", Rationale: "dangerous", Lang: "en"})
	if !strings.Contains(en, "✗ bash · ask · deny → denied") || !strings.Contains(en, "risk: high") {
		t.Errorf("en note = %q", en)
	}
}

const escalationArgs = `{"command":"rm -rf ~/work/harness/scratch","sandbox_permissions":"require_escalated","justification":"cleanup outside the workspace"}`

// TestNetworkRequestedCoversWebTools: the tools whose whole effect is an
// outbound request must take the network path, not run on the strength of their
// description. They execute in this process, so the gate is the only boundary
// they have.
func TestNetworkRequestedCoversWebTools(t *testing.T) {
	for _, tool := range []string{"webfetch", "websearch", "WebFetch", "webfetch "} {
		if !NetworkRequested(tool, nil) {
			t.Errorf("NetworkRequested(%q) = false, want true", tool)
		}
	}
	if NetworkRequested("bash", json.RawMessage(`{"command":"ls"}`)) {
		t.Error("bash without network:on must not count as a network request")
	}
	if !NetworkRequested("bash", json.RawMessage(`{"command":"git push","network":"on"}`)) {
		t.Error("bash with network:on must count as a network request")
	}
}

// TestPermissionGateWebFetchAsksForNetwork: a fetch the reviewer does not
// authorize takes the network question to the person — the honest version of
// "the gate decides execution AND network" — and it is the network question
// (not a no-sandbox one), because those tools never had a sandbox to lose.
func TestPermissionGateWebFetchAsksForNetwork(t *testing.T) {
	rec := &noteRecorder{}
	var gotReq ApprovalRequest
	opts := GateOpts{
		Classifier:     &stubClassifier{v: Verdict{Level: Allow, Risk: "low", Authorization: "medium", Reasons: []string{"reads a public page"}}},
		Sandboxed:      func(name string) bool { return name == "bash" },
		RequireSandbox: true,
		Notify:         rec.emit,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			gotReq = req
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("webfetch", `{"url":"https://example.com"}`))
	if dec == nil || dec.Block {
		t.Fatalf("an approved fetch should run, got %+v", dec)
	}
	if gotReq.Kind != ApprovalNetwork {
		t.Fatalf("kind = %q, want %q (the network question, not no-sandbox)", gotReq.Kind, ApprovalNetwork)
	}
	if gotReq.Mode != "auto" || gotReq.Level != Allow {
		t.Errorf("request mode/level = %q/%v, want auto/allow", gotReq.Mode, gotReq.Level)
	}
}

// TestPermissionGateWebFetchNetworkAuthorizedRunsSilently: when the reviewer
// answers the network question itself (the prompt tells it to for these tools),
// the fetch runs without a dialog — the person is asked only when the answer is
// missing or a deny.
func TestPermissionGateWebFetchNetworkAuthorizedRunsSilently(t *testing.T) {
	asked := false
	rec := &noteRecorder{}
	opts := GateOpts{
		Classifier: &stubClassifier{v: Verdict{
			Level: Allow, Risk: "low", Authorization: "high",
			Reasons: []string{"the user asked for this search"}, Network: true,
		}},
		Sandboxed:      func(name string) bool { return name == "bash" },
		RequireSandbox: true,
		Notify:         rec.emit,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			asked = true
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("websearch", `{"query":"terminal-browser"}`))
	if dec == nil || dec.Block {
		t.Fatalf("an authorized search should run, got %+v", dec)
	}
	if asked {
		t.Error("a network answer from the reviewer must not open a dialog")
	}
}

// TestReviewSystemPromptCoversWebToolNetwork pins the other half of that
// contract: the prompt must ask the reviewer for the network answer on the
// in-process tools. Without it the reviewer can never authorize a search, and
// every call falls through to the person.
func TestReviewSystemPromptCoversWebToolNetwork(t *testing.T) {
	p := reviewSystemPrompt("en")
	if !strings.Contains(p, "webfetch and websearch") {
		t.Error("prompt does not name the in-process network tools")
	}
	if !strings.Contains(p, "always for webfetch and websearch") {
		t.Error("prompt does not require the network answer for the web tools")
	}
	if strings.Contains(p, "Only include network if network:on was requested") {
		t.Error("prompt still scopes the network answer to network:on alone")
	}
}

func escalationGateOpts(rec *noteRecorder) GateOpts {
	return GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Sandbox, Risk: "high", Authorization: "low", Reasons: []string{"recursive delete outside the workspace"}}},
		Sandboxed:  func(name string) bool { return name == "bash" },
		Notify:     rec.emit,
	}
}

// TestPermissionGateEscalationAutoContainsWithNothingToOffer locks the mode
// rule: an escape request is settled by review under auto. The reviewer graded
// the call sandbox-tier, so it runs contained — and with no path to re-admit
// there is nothing to ask about, so no dialog opens. Interrupting the person
// with no answer to offer would break the one promise auto makes.
func TestPermissionGateEscalationAutoContainsWithNothingToOffer(t *testing.T) {
	rec := &noteRecorder{}
	asked := false
	opts := escalationGateOpts(rec)
	opts.Confirm = func(_ context.Context, _ ApprovalRequest) ApprovalAnswer {
		asked = true
		return ApprovalAnswer{Approve: true, Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	// No path in the command or the justification: nothing to re-admit.
	dec := gate(context.Background(), toolCall("bash", `{"command":"true","sandbox_permissions":"require_escalated","justification":"please"}`))
	if dec == nil || dec.Block || !dec.Sandbox || dec.Direct {
		t.Fatalf("auto must contain an escape request, got %+v", dec)
	}
	if asked {
		t.Error("auto mode must not open a dialog when there is no path to offer")
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteSandboxed || rec.notes[0].Mode != "auto" {
		t.Fatalf("notes = %+v, want one NoteSandboxed stamped with the mode", rec.notes)
	}
	if !strings.Contains(rec.notes[0].Rationale, "containable") {
		t.Errorf("rationale = %q, want it to say the escape was declined", rec.notes[0].Rationale)
	}
}

// TestPermissionGateEscalationAutoReAdmitsWhenContained: a contained call that
// asked to leave the sandbox for a path will fail on that very path, so the
// person is still offered it back — and a "no" changes nothing about auto's
// default: the call runs contained, with one note and no "denied by the user",
// because nothing was denied.
func TestPermissionGateEscalationAutoReAdmitsWhenContained(t *testing.T) {
	grantstest.Install(t)

	// A yes: the path is re-admitted inside the sandbox and the call runs there.
	rec := &noteRecorder{}
	var gotReq ApprovalRequest
	opts := escalationGateOpts(rec) // grades Sandbox: containable
	opts.Cwd = t.TempDir()
	opts.Confirm = func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
		gotReq = req
		return ApprovalAnswer{Approve: true, Answered: true, ReadRoot: req.ReadRoot}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox || dec.Direct {
		t.Fatalf("a re-admitted path must run the call contained, got %+v", dec)
	}
	if gotReq.Kind != ApprovalReAdmit || gotReq.ReadRoot == "" {
		t.Fatalf("request = %+v, want the re-admission question for a named path", gotReq)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteSandboxed {
		t.Fatalf("notes = %+v, want exactly one containment note", rec.notes)
	}
	if !strings.Contains(rec.notes[0].Rationale, "readable") {
		t.Errorf("rationale = %q, want the re-admitted path named", rec.notes[0].Rationale)
	}
	found := false
	for _, root := range grants.ReadRoots() {
		if root == gotReq.ReadRoot {
			found = true
		}
	}
	if !found {
		t.Errorf("read roots = %v, want the re-admitted path in the table the sandbox reads", grants.ReadRoots())
	}

	// A no: containment stands, once asked, with no denial recorded.
	rec2 := &noteRecorder{}
	asked := 0
	opts2 := escalationGateOpts(rec2)
	opts2.Cwd = t.TempDir()
	opts2.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		asked++
		return ApprovalAnswer{Answered: true}
	}
	gate2 := PermissionGate(permissions.New(permissions.Auto), opts2)
	if dec := gate2(context.Background(), toolCall("bash", escalationArgs)); dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("a refused re-admission must leave containment in place, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("asked = %d, want one question", asked)
	}
	if len(rec2.notes) != 1 || rec2.notes[0].Kind != NoteSandboxed {
		t.Fatalf("notes = %+v, want one containment note and no denial", rec2.notes)
	}
}

// TestPermissionGateEscalationAskModePrompts: under ask the person answers, and
// the request carries the mode and the grade so the dialog's decision line can
// say which mode is asking and what it is about.
func TestPermissionGateEscalationAskModePrompts(t *testing.T) {
	rec := &noteRecorder{}
	var gotReq ApprovalRequest
	opts := escalationGateOpts(rec)
	opts.Confirm = func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
		gotReq = req
		return ApprovalAnswer{Approve: true, Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Ask), opts)
	if dec := gate(context.Background(), toolCall("bash", escalationArgs)); dec == nil || !dec.Direct || dec.Block || dec.Sandbox {
		t.Fatalf("approved escalation needs a Direct grant without a sandbox bit, got %+v", dec)
	}
	if gotReq.Kind != ApprovalEscalation || gotReq.Mode != "ask" || gotReq.Level != Sandbox {
		t.Fatalf("request = %+v, want kind escalation, mode ask, level sandbox", gotReq)
	}
	if gotReq.Justification != "cleanup outside the workspace" {
		t.Errorf("justification = %q, want the model's reason", gotReq.Justification)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteApproved || rec.notes[0].Mode != "ask" {
		t.Fatalf("notes = %+v, want one NoteApproved", rec.notes)
	}
}

// TestPermissionGateEscalationContainsAnswerReContains: the dialog's default
// answer — "keep it in the sandbox" — runs the call contained even though the
// model asked to leave, and no Direct grant rides along.
func TestPermissionGateEscalationContainsAnswerReContains(t *testing.T) {
	rec := &noteRecorder{}
	opts := escalationGateOpts(rec)
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		return ApprovalAnswer{Approve: true, Contained: true, Answered: true}
	}
	gate := PermissionGate(permissions.New(permissions.Ask), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox || dec.Direct {
		t.Fatalf("the contained answer must re-contain the call, got %+v", dec)
	}
}

// TestPermissionGateEscalationAutoDirectFromReview: the one way auto lets a call
// out is the reviewer saying so — and it announces it, so a run outside the
// sandbox is never invisible.
func TestPermissionGateEscalationAutoDirectFromReview(t *testing.T) {
	rec := &noteRecorder{}
	opts := escalationGateOpts(rec)
	opts.Classifier = &stubClassifier{v: Verdict{
		Level: Allow, Direct: true, Risk: "medium", Authorization: "high",
		Reasons: []string{"the user asked for this path in the prompt"},
	}}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || !dec.Direct || dec.Block {
		t.Fatalf("a reviewer Direct grant must run the call bare, got %+v", dec)
	}
	if len(rec.notes) != 1 || rec.notes[0].Kind != NoteEscaped {
		t.Fatalf("notes = %+v, want one NoteEscaped", rec.notes)
	}
}

// TestPermissionGateEscalationAutoDeclinedNamesTheSmallerFix: a refusal under
// auto is not a dead end. A bare Allow is not a Direct grant and leaves nothing
// to contain, so the escape is refused — and the message hands back the
// least-privilege route (re-admit the path inside the sandbox) instead of
// repeating the request that just failed.
func TestPermissionGateEscalationAutoDeclinedNamesTheSmallerFix(t *testing.T) {
	rec := &noteRecorder{}
	opts := escalationGateOpts(rec)
	opts.Classifier = &stubClassifier{v: Verdict{
		Level: Allow, Risk: "low", Authorization: "unknown",
		Reasons: []string{"builds or tests, writing only caches and artifacts"},
	}}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || !dec.Block {
		t.Fatalf("an allow grade without a Direct grant must refuse the escape, got %+v", dec)
	}
	got := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(got, "switch /permissions to ask") {
		t.Errorf("refusal must keep the mode-switch advice, got %q", got)
	}
	if !strings.Contains(got, "/boundary readable add") || !strings.Contains(got, "GOLDER_SANDBOX_READABLE") {
		t.Errorf("refusal must name the read re-admission fix, got %q", got)
	}
	// escalationArgs names a path (~/work/harness/scratch), so the advice points
	// at the operand the request carried rather than a placeholder.
	if !strings.Contains(got, "scratch") {
		t.Errorf("refusal should name the path the request carried, got %q", got)
	}
}

// TestPermissionGateEscalationAutoOffersReadReAdmission: an escape auto refuses
// is not necessarily a dead end. When the request names a path, the gate asks
// whether to bring that path back INSIDE the sandbox — the smaller answer than
// running the call bare — and a yes runs the call contained with the path
// readable. The grant lands in the one table the sandbox itself reads, so the
// next call sees it too.
func TestPermissionGateEscalationAutoOffersReadReAdmission(t *testing.T) {
	grantstest.Install(t)
	rec := &noteRecorder{}
	var gotReq ApprovalRequest
	opts := escalationGateOpts(rec)
	opts.Cwd = t.TempDir()
	opts.Classifier = &stubClassifier{v: Verdict{
		Level: Allow, Risk: "low", Authorization: "unknown",
		Reasons: []string{"builds or tests, writing only caches and artifacts"},
	}}
	opts.Confirm = func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
		gotReq = req
		return ApprovalAnswer{Approve: true, Answered: true, ReadRoot: req.ReadRoot}
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox || dec.Direct {
		t.Fatalf("a re-admitted path must run the call contained, got %+v", dec)
	}
	if gotReq.Kind != ApprovalReAdmit {
		t.Fatalf("kind = %q, want %q (the re-admission question, not the escape)", gotReq.Kind, ApprovalReAdmit)
	}
	if gotReq.ReadRoot == "" || !strings.Contains(gotReq.ReadRoot, "scratch") {
		t.Errorf("ReadRoot = %q, want the path the request carried", gotReq.ReadRoot)
	}
	found := false
	for _, root := range grants.ReadRoots() {
		if root == gotReq.ReadRoot {
			found = true
		}
	}
	if !found {
		t.Errorf("read roots = %v, want the re-admitted path in the table the sandbox reads", grants.ReadRoots())
	}
	last := rec.notes[len(rec.notes)-1]
	if last.Kind != NoteSandboxed || !strings.Contains(last.Rationale, "readable") {
		t.Errorf("note = %+v, want the contained note naming the readable path", last)
	}
}

// TestPermissionGateEscalationReadmitAskedOnce: a refused re-admission is an
// answer, and the retry loop the refusal message already breaks must not become
// a nag — the question is asked once per path per run.
func TestPermissionGateEscalationReadmitAskedOnce(t *testing.T) {
	grantstest.Install(t)
	asked := 0
	opts := escalationGateOpts(&noteRecorder{})
	opts.Cwd = t.TempDir()
	opts.Classifier = &stubClassifier{v: Verdict{Level: Allow, Risk: "low", Authorization: "unknown"}}
	opts.Confirm = func(context.Context, ApprovalRequest) ApprovalAnswer {
		asked++
		return ApprovalAnswer{Answered: true} // refused
	}
	gate := PermissionGate(permissions.New(permissions.Auto), opts)
	call := toolCall("bash", escalationArgs)
	if dec := gate(context.Background(), call); dec == nil || !dec.Block {
		t.Fatalf("a refused re-admission must keep the refusal, got %+v", dec)
	}
	if asked != 1 {
		t.Fatalf("asked = %d, want the question asked once", asked)
	}
	// A retry of the same request does not re-open a question already answered,
	// and the refusal still names the smaller fix.
	dec := gate(context.Background(), call)
	if dec == nil || !dec.Block {
		t.Fatalf("retry must stay refused, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("asked = %d after a retry, want one question per path", asked)
	}
	if got := agentcore.ContentToText(*dec.Content); !strings.Contains(got, "/boundary readable add") {
		t.Errorf("refusal = %q, want the smaller fix named", got)
	}
}

// TestPathWriteNeed: the signal that decides which row leads the dialog. A path
// that is not there can only have been needed for writing; otherwise the command
// decides, and anything unrecognizable stays "read" — the narrower grant.
func TestPathWriteNeed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-yet")
	existing := t.TempDir()
	cases := []struct {
		name    string
		path    string
		summary string
		want    bool
	}{
		{"no path", "", "mkdir -p /x", false},
		{"missing path is a write", missing, "cat " + missing, true},
		{"mkdir on an existing dir", existing, "mkdir -p " + existing, true},
		{"plain read", existing, "ls " + existing, false},
		{"redirect", existing, "go build -o " + existing + "/x 2> " + existing + "/err", true},
	}
	for _, c := range cases {
		if got := PathWriteNeed(c.path, c.summary); got != c.want {
			t.Errorf("%s: PathWriteNeed(%q, %q) = %v, want %v", c.name, c.path, c.summary, got, c.want)
		}
	}
}

// TestApplyReAdmittedPath pins the one answer→grant mapping both triggers share:
// a read row registers a subtree read grant, a write row needs no registration
// here (the write dialog made it), and a refusal grants nothing.
func TestApplyReAdmittedPath(t *testing.T) {
	grantstest.Install(t)

	how, ok := ApplyReAdmittedPath(ApprovalAnswer{Approve: true, Answered: true, ReadRoot: "/Volumes/KIOXIA/build"}, "en")
	if !ok || !strings.Contains(how, "readable") {
		t.Fatalf("read answer = %q ok=%v, want a readable phrase", how, ok)
	}
	found := false
	for _, root := range grants.ReadRoots() {
		if root == "/Volumes/KIOXIA/build" {
			found = true
		}
	}
	if !found {
		t.Errorf("read roots = %v, want the granted path in the table the sandbox reads", grants.ReadRoots())
	}

	if how, ok := ApplyReAdmittedPath(ApprovalAnswer{Approve: true, Answered: true, WritableRoot: "/Volumes/KIOXIA/build"}, "en"); !ok || !strings.Contains(how, "writable") {
		t.Errorf("write answer = %q ok=%v, want a writable phrase", how, ok)
	}
	if _, ok := ApplyReAdmittedPath(ApprovalAnswer{Answered: true}, "en"); ok {
		t.Error("a refusal must not report a grant")
	}
}

// TestOnceSet: the memo behind every path-scoped question — the first sighting
// asks, later ones do not, and an empty key is not a question at all.
func TestOnceSet(t *testing.T) {
	var s OnceSet
	if s.First("") {
		t.Error("an empty key must never be asked about")
	}
	if !s.First("/a") {
		t.Error("the first sighting must report true")
	}
	if s.First("/a") {
		t.Error("a second sighting must report false")
	}
	if !s.First("/b") {
		t.Error("a different key is a new question")
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
	gate := PermissionGate(permissions.New(permissions.Ask), opts)
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
	gate := PermissionGate(permissions.New(permissions.Ask), opts)
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
	gate = PermissionGate(permissions.New(permissions.Ask), opts)
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

// TestPermissionGateEscalationStdin: under ask the REPL puts the escape question
// on stdin — y runs the call unisolated, n leaves it contained — while auto
// never asks at all, on stdin or anywhere else.
func TestPermissionGateEscalationStdin(t *testing.T) {
	var out bytes.Buffer
	opts := escalationGateOpts(&noteRecorder{})
	opts.In = bufio.NewReader(strings.NewReader("y\n"))
	opts.Out = &out
	gate := PermissionGate(permissions.New(permissions.Ask), opts)
	if dec := gate(context.Background(), toolCall("bash", escalationArgs)); dec == nil || !dec.Direct || dec.Block {
		t.Fatalf("answering y must grant a one-call Direct escape, got %+v", dec)
	}
	if !strings.Contains(out.String(), "outside the sandbox") {
		t.Errorf("prompt must spell out that approval runs the call outside the sandbox:\n%s", out.String())
	}
	// The stdin prompt leads with the same decision line as the dialog and the
	// notes: the person reading it learns which mode is asking before anything
	// else.
	if !strings.Contains(out.String(), "bash · ask · sandbox → your call · sandbox escape") {
		t.Errorf("stdin prompt must open with the decision line:\n%s", out.String())
	}
	// No answer means containment, never a bare run.
	var out2 bytes.Buffer
	opts = escalationGateOpts(&noteRecorder{})
	opts.In = bufio.NewReader(strings.NewReader("n\n"))
	opts.Out = &out2
	gate = PermissionGate(permissions.New(permissions.Ask), opts)
	dec := gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox {
		t.Fatalf("answering n must fall back to the sandbox, got %+v", dec)
	}

	// Auto decides by review: the typed answer is never read.
	var auto bytes.Buffer
	opts = escalationGateOpts(&noteRecorder{})
	opts.In = bufio.NewReader(strings.NewReader("y\n"))
	opts.Out = &auto
	gate = PermissionGate(permissions.New(permissions.Auto), opts)
	dec = gate(context.Background(), toolCall("bash", escalationArgs))
	if dec == nil || dec.Block || !dec.Sandbox || dec.Direct {
		t.Fatalf("auto must contain without prompting, got %+v", dec)
	}
	if strings.Contains(auto.String(), "Allow?") {
		t.Errorf("auto mode must not put the escape question on stdin:\n%s", auto.String())
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
		if got := escalationRequested(json.RawMessage(c.args)); got != c.want {
			t.Errorf("escalationRequested(%s) = %v, want %v", c.args, got, c.want)
		}
	}
}

// escalationStdinOpts is the stdin-prompt shape of an escalation in ask mode:
// the classifier grades Sandbox, the layer can isolate bash, and a grant
// callback is wired so the `w`/`s` answers exist.
func escalationStdinOpts(rec *noteRecorder, in string, grant func(root string, persist bool) (bool, string)) GateOpts {
	opts := escalationGateOpts(rec)
	opts.Cwd = "/Users/someone/project"
	// Both are needed for the stdin prompt: interactive means In != nil AND
	// Out != nil, so a half-wired opts silently fails closed instead.
	opts.In = bufio.NewReader(strings.NewReader(in))
	opts.Out = io.Discard
	opts.GrantWritable = grant
	return opts
}

// TestPromptRiskOffersWriteGrant pins the ladder the REPL prints for an
// escalation: the plain y/N when no grant is possible, the four answers when a
// plausible path can be re-admitted, and no row when the path is one the
// command could already write.
func TestPromptRiskOffersWriteGrant(t *testing.T) {
	rec := &noteRecorder{}
	const want = "\nAllow? [y]es (runs outside the sandbox) / [n]o / [w]rites to /Volumes/KIOXIA/build/out (this session)" +
		" / [s]ave writes to /Volumes/KIOXIA/build/out (remembered) [y/N/w/s]: "

	args := `{"command":"go build -o /Volumes/KIOXIA/build/out","sandbox_permissions":"require_escalated","justification":"the cache lives there"}`
	call := toolCall("bash", args)
	v := Verdict{Level: Sandbox, Risk: "high", Authorization: "low", Reasons: []string{"writes outside the workspace"}}

	// No callback: the prompt stays y/N, because an option that silently
	// forgets would be worse than an absent one.
	var out bytes.Buffer
	opts := escalationStdinOpts(rec, "n\n", nil)
	opts.Out = &out
	if ans := promptRisk(context.Background(), opts, permissions.Ask.String(), call, v, ApprovalEscalation); ans.allow {
		t.Error("denied prompt returned allow")
	}
	if got := out.String(); strings.Contains(got, "[w]rites") || !strings.Contains(got, "[y/N]") {
		t.Errorf("prompt without a grant callback = %q", got)
	}

	// With a callback and a plausible path: all four answers.
	out.Reset()
	opts = escalationStdinOpts(rec, "n\n", func(string, bool) (bool, string) { return true, "" })
	opts.Out = &out
	promptRisk(context.Background(), opts, permissions.Ask.String(), call, v, ApprovalEscalation)
	if got := out.String(); !strings.Contains(got, want) {
		t.Errorf("prompt = %q, want it to carry %q", got, want)
	}

	// A path the command can already write offers no grant: the workspace is
	// writable by definition.
	out.Reset()
	opts = escalationStdinOpts(rec, "n\n", func(string, bool) (bool, string) { return true, "" })
	opts.Out = &out
	inWorkspace := toolCall("bash", `{"command":"go build -o `+opts.Cwd+`/out","sandbox_permissions":"require_escalated","justification":"local"}`)
	promptRisk(context.Background(), opts, permissions.Ask.String(), inWorkspace, v, ApprovalEscalation)
	if got := out.String(); strings.Contains(got, "[w]rites") {
		t.Errorf("an already-writable path must not be offered: %q", got)
	}
}

// TestPromptRiskWriteAnswers pins what each answer does: w grants for the
// session, s grants and persists, y runs unisolated, anything else denies.
func TestPromptRiskWriteAnswers(t *testing.T) {
	const args = `{"command":"go build -o /Volumes/KIOXIA/build/out","sandbox_permissions":"require_escalated","justification":"cache"}`
	call := toolCall("bash", args)
	v := Verdict{Level: Sandbox, Risk: "high", Authorization: "low", Reasons: []string{"outside"}}

	for _, c := range []struct {
		in        string
		wantRoot  string
		wantPers  bool
		wantAllow bool
	}{
		{"y\n", "", false, true},
		{"w\n", "/Volumes/KIOXIA/build/out", false, true},
		{"s\n", "/Volumes/KIOXIA/build/out", true, true},
		{"\n", "", false, false},
	} {
		var gotRoot string
		var gotPersist bool
		grant := func(root string, persist bool) (bool, string) {
			gotRoot, gotPersist = root, persist
			return true, ""
		}
		opts := escalationStdinOpts(&noteRecorder{}, c.in, grant)
		var out bytes.Buffer
		opts.Out = &out
		ans := promptRisk(context.Background(), opts, permissions.Ask.String(), call, v, ApprovalEscalation)
		if ans.allow != c.wantAllow || ans.writableRoot != c.wantRoot || ans.persisted != c.wantPers {
			t.Errorf("%q -> %+v, want allow=%v root=%q persisted=%v",
				c.in, ans, c.wantAllow, c.wantRoot, c.wantPers)
		}
		if c.wantRoot != "" && (gotRoot != c.wantRoot || gotPersist != c.wantPers) {
			t.Errorf("%q granted (%q, persist=%v), want (%q, %v)", c.in, gotRoot, gotPersist, c.wantRoot, c.wantPers)
		}
	}
}

// TestGateWriteGrantKeepsTheCallContained is the reason `w`/`s` exist: the
// user re-admits one path, so the call runs ISOLATED with that path writable,
// rather than unisolated as a plain `y` would approve. The note must say so,
// and a remembered grant must not be described as session-only.
func TestGateWriteGrantKeepsTheCallContained(t *testing.T) {
	args := `{"command":"go build -o /Volumes/KIOXIA/build/out","sandbox_permissions":"require_escalated","justification":"cache"}`
	for _, c := range []struct {
		in           string
		wantSandbox  bool
		wantRemember bool
	}{
		{"w\n", true, false},
		{"s\n", true, true},
		{"y\n", false, false},
	} {
		rec := &noteRecorder{}
		opts := escalationStdinOpts(rec, c.in, func(string, bool) (bool, string) { return true, "" })
		gate := PermissionGate(permissions.New(permissions.Ask), opts)
		dec := gate(context.Background(), toolCall("bash", args))
		switch {
		case c.wantSandbox && (dec == nil || !dec.Sandbox):
			t.Errorf("%q: decision = %+v, want the sandbox bit (contained)", c.in, dec)
		case !c.wantSandbox && (dec == nil || !dec.Direct || dec.Block):
			t.Errorf("%q: decision = %+v, want an explicit Direct approval", c.in, dec)
		}
		if !c.wantSandbox {
			// A plain yes runs the call unisolated, so there is no containment
			// note to read.
			if len(rec.notes) != 0 {
				t.Errorf("%q: notes = %+v, want none for an unisolated approval", c.in, rec.notes)
			}
			continue
		}
		if len(rec.notes) != 1 {
			t.Fatalf("%q: notes = %+v, want one", c.in, rec.notes)
		}
		rationale := rec.notes[0].Rationale
		if !strings.Contains(rationale, "contained in the sandbox") {
			t.Errorf("%q: rationale = %q, want it to say the call stays contained", c.in, rationale)
		}
		if got := strings.Contains(rationale, "remembered"); got != c.wantRemember {
			t.Errorf("%q: rationale = %q, remembered=%v want %v", c.in, rationale, got, c.wantRemember)
		}
	}
}
