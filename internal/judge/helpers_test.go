package judge

// Shared fixtures for the judge tests. These used to live in gate_test.go and
// boundary_test.go alongside the legacy gate tests; they are the parts still
// used by the live ConsentGate and helper tests.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
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

// bashCall builds a bash call whose only argument is the command.
func bashCall(cmd string) agentcore.AgentToolCall {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return agentcore.AgentToolCall{Name: "bash", Arguments: args}
}

// noteRecorder captures the notes a gate emits.
type noteRecorder struct{ notes []Note }

func (r *noteRecorder) emit(n Note) { r.notes = append(r.notes, n) }

// readCall builds a read tool call for a path argument.
func readCall(tool, path string) agentcore.AgentToolCall {
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		panic(err)
	}
	return agentcore.AgentToolCall{Name: tool, Arguments: raw}
}

// userHome returns the REAL home directory (HOME may have been redirected by an
// earlier test), so a fixture can be placed under it — outside both $TMPDIR and
// the system whitelist — rather than inside either.
func userHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	return home
}

// readFixture gives a test a workspace, an outside directory holding a
// readable file, and a HOME of its own, so the broad-root policy is
// deterministic. Grants are cleared afterwards: the registry is process-wide.
func readFixture(t *testing.T) (workspace, outside, target string) {
	t.Helper()
	// The fixture must NOT live under $TMPDIR: the system whitelist exempts the
	// machine's temp root (the sandbox admits its own temp dir too), so a
	// fixture there would be silently exempt and every "outside" case would
	// pass for the wrong reason — or, as here, fail to ask at all. A fabricated
	// HOME under the real one puts it outside the whitelist, because $HOME is
	// deliberately absent from the list; that is exactly the boundary these
	// tests are about.
	home := filepath.Join(userHome(t), ".golder-test", t.Name())
	if err := os.MkdirAll(home, 0o755); err != nil {
		// A sandboxed run cannot reach $HOME: the sandbox read whitelist
		// excludes it by construction, and the fixture must live outside
		// $TMPDIR to be a real boundary case (see the comment above). Skip
		// rather than fail — the environment cannot host the fixture, which is
		// not the same as the behavior being wrong — and keep the sibling
		// fixture's rule, so both fail the same way everywhere.
		t.Skipf("cannot create fixture under $HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	workspace = filepath.Join(home, "workspace")
	outside = filepath.Join(home, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target = filepath.Join(outside, "a.go")
	if err := os.WriteFile(target, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grantstest.Install(t)
	return workspace, outside, target
}
