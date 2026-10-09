// Tests for the `!command` passthrough in the REPL: the loop intercepts it
// before any model dispatch, the command runs locally, its output is printed,
// and the settled record joins the shared context and the session file.

package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/execsess"
)

// TestREPLUserShellPassthrough drives "!echo repl-ok" through the real loop and
// verifies it runs locally (no model call), prints its output, records the
// codex-shaped user message in the context, and persists the turn.
func TestREPLUserShellPassthrough(t *testing.T) {
	// /bin/sh keeps the login-shell spawn fast and deterministic; the
	// passthrough itself prefers $SHELL.
	t.Setenv("SHELL", "/bin/sh")
	p := &replProvider{reply: "hi"}
	deps, store := newTestDeps(t, p)
	deps.sessions = execsess.NewManager()
	deps.cwd = t.TempDir()

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("!echo repl-ok\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL returned error: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("a ! line must not call the model, got %d calls", p.calls)
	}
	if got := out.String(); !strings.Contains(got, "repl-ok") {
		t.Errorf("command output not printed:\n%s", got)
	}
	if len(deps.agentCtx.Messages) != 1 {
		t.Fatalf("context messages = %d, want the passthrough record", len(deps.agentCtx.Messages))
	}
	um, ok := deps.agentCtx.Messages[0].(agentcore.UserMessage)
	if !ok {
		t.Fatalf("record is %T, want agentcore.UserMessage", deps.agentCtx.Messages[0])
	}
	d, ok := cli.ParseUserShellRecord(agentcore.ContentToText(um.Content))
	if !ok || d.Command != "echo repl-ok" || d.ExitCode != 0 || !strings.Contains(d.Output, "repl-ok") {
		t.Fatalf("record = %+v ok=%v, want `echo repl-ok` with its output", d, ok)
	}
	// The record was persisted, so it survives a restart like any other turn.
	_, entries, err := store.LoadEntries(deps.header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("persisted entries = %d, want 1", len(entries))
	}
}

// TestREPLBareBangShowsHint verifies a lone "!" prints the usage hint and
// neither runs nor records anything.
func TestREPLBareBangShowsHint(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)

	var out bytes.Buffer
	if err := runREPL(strings.NewReader("!\n/exit\n"), &out, deps); err != nil {
		t.Fatalf("runREPL returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Prefix a command with ! to run it locally") {
		t.Errorf("bare ! hint missing:\n%s", got)
	}
	if p.calls != 0 || len(deps.agentCtx.Messages) != 0 {
		t.Errorf("bare ! must not run or record anything (calls=%d messages=%d)",
			p.calls, len(deps.agentCtx.Messages))
	}
}
