// Tests for the /rewind command wiring: listing restore points and restoring
// files + conversation. The file-snapshot journal itself is tested in
// agenttool; here we exercise runRewind's REPL-level behavior over a replDeps.
package repl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
)

// /rewind with no argument lists the committed restore points.
func TestREPLRewindListsPoints(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = agenttool.NewFileSnapshotRecorder()

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Record(f)
	deps.snap.Commit("", "add feature X")

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind")
	got := out.String()
	if !strings.Contains(got, "restore points") || !strings.Contains(got, "add feature X") {
		t.Errorf("listing missing expected content:\n%s", got)
	}
}

// /rewind N restores the file to its baseline and resets the conversation when
// the point's leaf is empty (it was the session's first turn).
func TestREPLRewindRestoresFiles(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = agenttool.NewFileSnapshotRecorder()
	deps.agentCtx.Messages = agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}
	deps.persisted = 1

	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Record(f) // baseline "original"
	if err := os.WriteFile(f, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.snap.Commit("", "edit a.txt")

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind 1")

	if data, _ := os.ReadFile(f); string(data) != "original" {
		t.Errorf("file not restored: got %q, want original", string(data))
	}
	if len(deps.agentCtx.Messages) != 0 {
		t.Errorf("conversation not reset: %d messages remain", len(deps.agentCtx.Messages))
	}
	if len(deps.snap.Points()) != 0 {
		t.Errorf("journal not truncated after rewind")
	}
	if !strings.Contains(out.String(), "rewound to before point 1") {
		t.Errorf("missing confirmation:\n%s", out.String())
	}
}

// /rewind is unavailable when file tools are disabled (nil recorder).
func TestREPLRewindDisabled(t *testing.T) {
	p := &replProvider{reply: "hi"}
	deps, _ := newTestDeps(t, p)
	deps.snap = nil

	var out bytes.Buffer
	runRewind(&out, &deps, "/rewind")
	if !strings.Contains(out.String(), "unavailable") {
		t.Errorf("want unavailable message, got:\n%s", out.String())
	}
}
