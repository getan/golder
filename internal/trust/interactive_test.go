package trust

// Tests for the project-trust interactive glue (US-018, #134): the first-run
// prompt, the tool-call confirmation, the summary preview, and the
// BeforeToolCall gating hook. The trust store itself is covered in
// manager_test.go; here we exercise the interactive pieces.

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/runtime"
)

// pipeReader opens an OS pipe whose read end blocks until data arrives or
// the write end closes. Tests keep the write end open to simulate a user
// who never answers.
func pipeReader(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	return r, w
}

// newTrustManagerAt builds a Manager backed by path.
func newTrustManagerAt(t *testing.T, path string) *Manager {
	t.Helper()
	m, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// newTrustManager builds a Manager backed by a fresh temp file and returns the
// manager plus its path (so a test can reload to verify persistence).
func newTrustManager(t *testing.T) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust.json")
	return newTrustManagerAt(t, path), path
}

// readerOf wraps a string in the *bufio.Reader the trust prompts expect.
func readerOf(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

// TestEnsureTrustPromptTrustedThisDir verifies choice 1 + scope 1 (this
// directory) persists a Trusted decision for cwd.
func TestEnsureTrustPromptTrustedThisDir(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	EnsureTrustPrompt(&out, readerOf("1\n1\n"), mgr, cwd)

	if got := mgr.NearestTrustDecision(cwd); !got.Found || got.Decision != Trusted || got.Path != cwd {
		t.Errorf("after prompt, NearestTrustDecision(%q) = %+v, want Trusted/@cwd", cwd, got)
	}
	if !strings.Contains(out.String(), "First time in this directory") {
		t.Errorf("output missing prompt: %q", out.String())
	}
}

// TestEnsureTrustPromptRejectParent verifies choice 3 (reject) + scope 2
// (parent) persists an Untrusted decision for the parent directory.
func TestEnsureTrustPromptRejectParent(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	parent := filepath.Dir(cwd)
	EnsureTrustPrompt(&bytes.Buffer{}, readerOf("3\n2\n"), mgr, cwd)

	if got := mgr.NearestTrustDecision(cwd); !got.Found || got.Decision != Untrusted || got.Path != parent {
		t.Errorf("after reject-parent, NearestTrustDecision(%q) = %+v, want Untrusted/@parent", cwd, got)
	}
}

// TestEnsureTrustPromptJustOnce verifies choice 2 (just once) grants session
// trust without persisting: IsTrusted is true now but false after a reload.
func TestEnsureTrustPromptJustOnce(t *testing.T) {
	mgr, path := newTrustManager(t)
	cwd := t.TempDir()
	EnsureTrustPrompt(&bytes.Buffer{}, readerOf("2\n"), mgr, cwd)

	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after just-once, want true")
	}
	// Reload from the same file: session trust must not survive.
	m2 := newTrustManagerAt(t, path)
	if m2.IsTrusted(cwd) {
		t.Error("reload IsTrusted(cwd) = true, want false (session trust must not persist)")
	}
}

// TestEnsureTrustPromptSkipsWhenDecided verifies that when a decision already
// exists, EnsureTrustPrompt is a no-op: it writes nothing and reads nothing.
func TestEnsureTrustPromptSkipsWhenDecided(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	if err := mgr.SetDecision(cwd, Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	var out bytes.Buffer
	// Empty reader: if the prompt tried to read it would block/EOF; it must not.
	EnsureTrustPrompt(&out, readerOf(""), mgr, cwd)
	if out.Len() != 0 {
		t.Errorf("EnsureTrustPrompt wrote %q when a decision existed, want no output", out.String())
	}
}

// TestEstablishTrustApprove verifies --approve grants session trust up front
// without prompting: IsTrusted is true immediately, nothing is written, and the
// (empty) reader is never consumed. Session trust must not persist across a
// reload — --approve is per-run, not a saved decision.
func TestEstablishTrustApprove(t *testing.T) {
	mgr, path := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	EstablishTrust(&out, readerOf(""), mgr, cwd, true)

	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after --approve, want true")
	}
	if out.Len() != 0 {
		t.Errorf("EstablishTrust wrote %q with --approve, want no prompt", out.String())
	}
	m2 := newTrustManagerAt(t, path)
	if m2.IsTrusted(cwd) {
		t.Error("reload IsTrusted(cwd) = true, want false (--approve must not persist)")
	}
}

// TestEstablishTrustWithoutApprove verifies that without --approve, EstablishTrust
// defers to the first-launch prompt (which runs for an undecided directory).
func TestEstablishTrustWithoutApprove(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	// "2\n" answers the just-once prompt; if the prompt did not run, this input
	// would be left unread and IsTrusted would stay false.
	EstablishTrust(&out, readerOf("2\n"), mgr, cwd, false)

	if !strings.Contains(out.String(), "First time in this directory") {
		t.Errorf("without --approve, expected the trust prompt; got %q", out.String())
	}
	if !mgr.IsTrusted(cwd) {
		t.Error("IsTrusted(cwd) = false after just-once via EstablishTrust, want true")
	}
}

// TestConfirmToolCall verifies the answer ladder's keys: y/n/a on every gated
// tool, plus r for the middle rung, which exists only on the local read tools.
func TestConfirmToolCall(t *testing.T) {
	call := agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"ls"}`)}
	cases := []struct {
		in   string
		want Answer
	}{
		{"y\n", Answer{Allow: true}},
		{"yes\n", Answer{Allow: true}},
		{"a\n", Answer{Allow: true, Always: true}},
		{"all\n", Answer{Allow: true, Always: true}},
		{"always\n", Answer{Allow: true, Always: true}},
		{"n\n", Answer{}},
		{"\n", Answer{}},
		{"garbage\n", Answer{}},
		// "r" is not an answer for a command: it falls through to denial
		// rather than granting a reads rung a command does not have.
		{"r\n", Answer{}},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if got := ConfirmToolCall(context.Background(), &out, readerOf(c.in), call); got != c.want {
			t.Errorf("ConfirmToolCall(bash, %q) = %+v, want %+v", c.in, got, c.want)
		}
	}

	read := agentcore.AgentToolCall{Name: "read", Arguments: []byte(`{"path":"a.go"}`)}
	for _, in := range []string{"r\n", "reads\n"} {
		var out bytes.Buffer
		want := Answer{Allow: true, Reads: true}
		if got := ConfirmToolCall(context.Background(), &out, readerOf(in), read); got != want {
			t.Errorf("ConfirmToolCall(read, %q) = %+v, want %+v", in, got, want)
		}
	}
	// The prompt offers the middle rung only where the grant reaches.
	var out bytes.Buffer
	ConfirmToolCall(context.Background(), &out, readerOf("n\n"), read)
	if !strings.Contains(out.String(), "[r]eads in this folder") {
		t.Errorf("a read prompt must offer the reads rung:\n%s", out.String())
	}
	out.Reset()
	ConfirmToolCall(context.Background(), &out, readerOf("n\n"), call)
	if strings.Contains(out.String(), "[r]eads") {
		t.Errorf("a command prompt must not offer the reads rung:\n%s", out.String())
	}
}

// TestGatesToolReadsGrant covers the middle rung's reach: a session reads
// grant stops the local read tools asking in that directory, while commands
// and network reads keep asking, and clearing the session trust revokes it.
func TestGatesToolReadsGrant(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	local := []string{"read", "view_image", "grep", "find", "ls"}

	for _, name := range local {
		if !mgr.GatesTool(name, cwd) {
			t.Errorf("without a grant, %q must be gated", name)
		}
	}
	mgr.SetSessionReads(cwd)
	for _, name := range local {
		if mgr.GatesTool(name, cwd) {
			t.Errorf("with a reads grant, %q must not be gated", name)
		}
	}
	for _, name := range []string{"bash", "apply_patch", "webfetch", "websearch"} {
		if !mgr.GatesTool(name, cwd) {
			t.Errorf("a reads grant must not cover %q", name)
		}
	}
	// The grant walks up like session trust, so a descendant directory is
	// covered too.
	if mgr.GatesTool("read", filepath.Join(cwd, "sub")) {
		t.Error("a reads grant must cover descendant directories")
	}
	// ClearSessionTrust revokes it: "no" to the directory cannot leave the
	// reads half standing.
	mgr.ClearSessionTrust(cwd)
	if !mgr.GatesTool("read", cwd) {
		t.Error("clearing session trust must revoke the reads grant")
	}
}

// TestBeforeToolCallReadsAnswer covers the REPL's middle rung end to end: an
// "r" answer on a read grants session reads only, so the next read is silent
// while a command still prompts (and can still be denied).
func TestBeforeToolCallReadsAnswer(t *testing.T) {
	cwd := t.TempDir()
	mgr, _ := newTrustManager(t)
	hook := BeforeToolCall(mgr, cwd, readerOf("r\n"), &bytes.Buffer{}, &sync.Mutex{})
	if dec := hook(context.Background(), agentcore.AgentToolCall{Name: "read", Arguments: []byte(`{"path":"a.go"}`)}); dec != nil {
		t.Fatalf("the approved read = %+v, want nil", dec)
	}
	if !mgr.ReadsTrusted(cwd) {
		t.Fatal("the reads answer must register a session grant")
	}
	if mgr.IsTrusted(cwd) {
		t.Error("the reads answer must not grant full session trust")
	}

	// A fresh prompt denies the command, which both proves the command still
	// asks and that the reads rung did not widen into "all tools".
	deny := BeforeToolCall(mgr, cwd, readerOf("n\n"), &bytes.Buffer{}, &sync.Mutex{})
	if dec := deny(context.Background(), agentcore.AgentToolCall{Name: "read"}); dec != nil {
		t.Errorf("after the reads grant, a read must not be gated, got %+v", dec)
	}
	if dec := deny(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"ls"}`)}); dec == nil || !dec.Block {
		t.Errorf("after the reads grant, a command must still ask (and deny) = %+v", dec)
	}
}

// TestConfirmToolCallInterrupt verifies a Ctrl+C during the prompt (the run
// context canceled while the read is blocked) denies immediately instead of
// trapping the user until they answer. The reader blocks on an open pipe so
// no input ever arrives.
func TestConfirmToolCallInterrupt(t *testing.T) {
	call := agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"ls"}`)}
	r, w := pipeReader(t)
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Answer, 1)
	go func() {
		var out bytes.Buffer
		done <- ConfirmToolCall(ctx, &out, bufio.NewReader(r), call)
	}()
	cancel()
	select {
	case got := <-done:
		if got != (Answer{}) {
			t.Fatalf("interrupted ConfirmToolCall = %+v, want the zero Answer", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted ConfirmToolCall did not return")
	}
}

// TestToolCallSummary verifies the one-line preview for each side-effect tool.
func TestToolCallSummary(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"bash", `{"command":"rm -rf /tmp/x"}`, "rm -rf /tmp/x"},
		{"apply_patch", `{"patch":"*** Begin Patch\n*** Delete File: /a/b.txt\n*** End Patch\n"}`, "patch touching /a/b.txt"},
		{"apply_patch", `{"patch":"garbage"}`, "patch: garbage"},
		{"read", `{"path":"internal/cli/tui/trustgate.go"}`, "read internal/cli/tui/trustgate.go"},
		{"view_image", `{"path":"docs/a.png"}`, "view docs/a.png"},
		{"ls", `{"path":"internal"}`, "list internal"},
		{"grep", `{"pattern":"StrictReads","path":"internal"}`, `grep "StrictReads" in internal`},
		{"find", `{"glob":"*.go","path":["agent","cmd"]}`, `find "*.go" in agent, cmd`},
		{"webfetch", `{"url":"https://example.com/x"}`, "fetch https://example.com/x"},
		{"websearch", `{"query":"golder trust gate"}`, `search "golder trust gate"`},
		{"bash", `{}`, ""},
		{"bash", ``, ""},
		{"bash", `not-json`, "not-json"},
	}
	for _, c := range cases {
		got := toolCallSummary(agentcore.AgentToolCall{Name: c.name, Arguments: []byte(c.args)})
		if got != c.want {
			t.Errorf("toolCallSummary(%s,%s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// TestToolCallSummaryTruncates verifies a very long command is truncated.
func TestToolCallSummaryTruncates(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := toolCallSummary(agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"` + long + `"}`)})
	if !strings.HasSuffix(got, " …") {
		t.Errorf("expected truncated output ending with ' …', got %q", got)
	}
	if len([]rune(got)) > 250 {
		t.Errorf("output not truncated: len=%d", len([]rune(got)))
	}
}

// TestTrustBeforeToolCallGating verifies the hook: nil manager -> nil hook;
// trusted dir -> allow; untrusted + deny -> block; untrusted + always -> allow
// and grant session trust so the next call is allowed without a prompt.
func TestTrustBeforeToolCallGating(t *testing.T) {
	cwd := t.TempDir()
	mu := &sync.Mutex{}
	call := agentcore.AgentToolCall{Name: "apply_patch", Arguments: []byte(
		`{"patch":"*** Begin Patch\n*** Delete File: /tmp/x\n*** End Patch\n"}`)}

	// nil manager -> no hook.
	if h := BeforeToolCall(nil, cwd, nil, nil, mu); h != nil {
		t.Error("nil manager should yield nil hook")
	}

	// Trusted dir: hook returns nil (allow) without prompting.
	mgr, _ := newTrustManager(t)
	if err := mgr.SetDecision(cwd, Trusted); err != nil {
		t.Fatalf("SetDecision: %v", err)
	}
	hook := BeforeToolCall(mgr, cwd, readerOf(""), &bytes.Buffer{}, mu)
	if dec := hook(context.Background(), call); dec != nil {
		t.Errorf("trusted dir: hook returned %+v, want nil", dec)
	}

	// Untrusted dir, user denies: hook blocks with an error result.
	mgr2, _ := newTrustManager(t)
	var out bytes.Buffer
	hook2 := BeforeToolCall(mgr2, cwd, readerOf("n\n"), &out, mu)
	dec := hook2(context.Background(), call)
	if dec == nil || !dec.Block {
		t.Errorf("untrusted + deny: hook returned %+v, want a Block decision", dec)
	}

	// Untrusted dir, user says "always": hook allows and grants session trust,
	// so a second call is allowed WITHOUT reading any more input.
	mgr3, _ := newTrustManager(t)
	var out3 bytes.Buffer
	// Only one line of input ("a\n"); a second prompt would block on EOF.
	hook3 := BeforeToolCall(mgr3, cwd, readerOf("a\n"), &out3, mu)
	if dec := hook3(context.Background(), call); dec != nil {
		t.Errorf("untrusted + always: hook returned %+v, want nil (allow)", dec)
	}
	if !mgr3.IsTrusted(cwd) {
		t.Error("after 'always', IsTrusted(cwd) = false, want true (session trust granted)")
	}
	// Second call: no input left, but session trust means no prompt.
	if dec := hook3(context.Background(), call); dec != nil {
		t.Errorf("second call after 'always': hook returned %+v, want nil (no re-prompt)", dec)
	}
}

// TestTrustBeforeToolCallStrictReads verifies the fork default: in an
// untrusted directory, data-ingestion tools (read, grep, find, ls, webfetch,
// websearch) are gated like side-effect tools, while pure in-memory tools
// (todo, schedule_*, goal_*, memory_*) are never gated.
func TestTrustBeforeToolCallStrictReads(t *testing.T) {
	cwd := t.TempDir()
	mu := &sync.Mutex{}

	// Strict (default): reads prompt; "y" allows without granting trust.
	mgr, _ := newTrustManager(t)
	if !mgr.StrictReads {
		t.Fatal("fork default StrictReads = false, want true")
	}
	hook := BeforeToolCall(mgr, cwd, readerOf("y\ny\ny\ny\ny\ny\n"), &bytes.Buffer{}, mu)
	for _, name := range []string{"read", "grep", "find", "ls", "webfetch", "websearch"} {
		if dec := hook(context.Background(), agentcore.AgentToolCall{Name: name}); dec != nil {
			t.Errorf("strict read tool %q was blocked on allow (%+v), want nil", name, dec)
		}
	}
	if mgr.IsTrusted(cwd) {
		t.Error("'y' must not grant session trust")
	}
	// Strict: "n" blocks a read.
	hookDeny := BeforeToolCall(mgr, cwd, readerOf("n\n"), &bytes.Buffer{}, mu)
	if dec := hookDeny(context.Background(), agentcore.AgentToolCall{Name: "read"}); dec == nil || !dec.Block {
		t.Errorf("strict deny: hook returned %+v, want a Block decision", dec)
	}
	// Strict: in-memory tools are never gated.
	hookMem := BeforeToolCall(mgr, cwd, readerOf(""), &bytes.Buffer{}, mu)
	for _, name := range []string{"todo", "schedule_create", "goal_complete", "memory_search"} {
		if dec := hookMem(context.Background(), agentcore.AgentToolCall{Name: name}); dec != nil {
			t.Errorf("in-memory tool %q was gated (%+v), want nil", name, dec)
		}
	}

	// Relaxed: reads pass through untouched.
	mgr2, _ := newTrustManager(t)
	mgr2.StrictReads = false
	hook2 := BeforeToolCall(mgr2, cwd, readerOf(""), &bytes.Buffer{}, mu)
	for _, name := range []string{"read", "grep", "find", "ls", "todo", "webfetch", "websearch"} {
		if dec := hook2(context.Background(), agentcore.AgentToolCall{Name: name}); dec != nil {
			t.Errorf("relaxed tool %q was gated (%+v), want nil", name, dec)
		}
	}
}

// TestRegisterTrustCommand exercises the /trust action closure: status on an
// undecided dir, on (default) persists Trusted, once grants session trust, off
// revokes session trust AND persists Untrusted (the M2 regression - without
// ClearSessionTrust, an active "always"/once grant would keep IsTrusted true
// until restart), and an unknown arg yields usage.
func TestRegisterTrustCommand(t *testing.T) {
	cwd := t.TempDir()
	mgr, _ := newTrustManager(t)
	reg := runtime.NewSlashRegistry()
	RegisterCommand(reg, mgr, cwd)
	cmd, ok := reg.Lookup("trust")
	if !ok {
		t.Fatal("/trust command not registered")
	}

	if got := cmd.Action("status"); !strings.Contains(got, "undecided") {
		t.Errorf("status on undecided dir = %q, want 'undecided'", got)
	}

	// on (default arg) persists Trusted for cwd.
	if got := cmd.Action(""); !strings.Contains(got, "trusted") {
		t.Errorf("on = %q, want 'trusted'", got)
	}
	if res := mgr.NearestTrustDecision(cwd); !res.Found || res.Decision != Trusted {
		t.Errorf("after on, nearest = %+v, want Trusted/@cwd", res)
	}

	// Fresh dir: once grants session trust (in-memory), off must revoke it and
	// persist Untrusted so IsTrusted is false immediately.
	cwd2 := t.TempDir()
	mgr2, _ := newTrustManager(t)
	reg2 := runtime.NewSlashRegistry()
	RegisterCommand(reg2, mgr2, cwd2)
	cmd2, _ := reg2.Lookup("trust")
	cmd2.Action("once")
	if !mgr2.IsTrusted(cwd2) {
		t.Error("after once, IsTrusted = false, want true")
	}
	if got := cmd2.Action("off"); !strings.Contains(got, "untrusted") {
		t.Errorf("off = %q, want 'untrusted'", got)
	}
	if mgr2.IsTrusted(cwd2) {
		t.Error("after off, IsTrusted = true, want false (session grant must be revoked)")
	}
	if res := mgr2.NearestTrustDecision(cwd2); !res.Found || res.Decision != Untrusted {
		t.Errorf("after off, nearest = %+v, want Untrusted/@cwd", res)
	}

	// Unknown arg yields usage text.
	if got := cmd.Action("bogus"); !strings.Contains(got, "usage") {
		t.Errorf("unknown arg = %q, want 'usage'", got)
	}
}
