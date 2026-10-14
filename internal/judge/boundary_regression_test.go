package judge

// The cross-layer boundary invariant: a shell command that reads outside the
// workspace must be seen by at least one layer — the boundary gate asks about
// the path, or the command is not recognized as read-only and the reviewer
// judges it. Both layers being silent is the failure mode this file exists to
// prevent, and it is invisible without a test: each layer looks correct on its
// own, and the hole is in the gap between the operand scanner's coverage and the
// whitelist's membership.
//
// The commands below are the shapes that actually fell through that gap while
// the recognizer was being built: `cksum /outside/x` (whitelisted, operand not
// scanned) and `cd /outside && ls` (the operand was never examined at all),
// plus the flags that name a second file the scanner used to miss.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// boundaryFixture builds a workspace and an outside directory under $HOME.
//
// Deliberately NOT t.TempDir(): the machine's temp root is on the system read
// whitelist (the sandbox admits it), so a fixture there is exempt by
// construction and every case below would pass for the wrong reason.
func boundaryFixture(t *testing.T) (workspace, outside string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	base := filepath.Join(home, ".golder-test", t.Name())
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Skipf("cannot create fixture under $HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	workspace = filepath.Join(base, "workspace")
	outside = filepath.Join(base, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{
		"a.go":    "package a\n",
		"pat":     "TODO\n",
		"list":    "x\x00",
		"magic":   "0 string x y\n",
		"dates":   "2026-01-01\n",
		"inside":  "package inside\n",
		"notes":   "notes\n",
		"outside": "package outside\n",
	} {
		path := filepath.Join(outside, name)
		if name == "inside" {
			path = filepath.Join(workspace, name)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	grantstest.Install(t)
	return workspace, outside
}

// TestOutsideReadsAreAlwaysSeen is the invariant, exercised through the real
// chain in the order a driver builds it: the boundary gate in front of the
// permission gate, with a reviewer that records being consulted.
func TestOutsideReadsAreAlwaysSeen(t *testing.T) {
	workspace, outside := boundaryFixture(t)

	cases := []struct {
		name string
		cmd  string
	}{
		// Plain reads: the scanner sees the operand and the gate asks.
		{"cat", "cat " + outside + "/a.go"},
		{"ls", "ls " + outside},
		{"rg", "rg -n TODO " + outside},
		{"cksum", "cksum " + outside + "/a.go"},
		{"base32", "base32 " + outside + "/a.go"},
		{"du", "du -sh " + outside},
		{"readlink", "readlink " + outside + "/a.go"},
		{"realpath", "realpath " + outside + "/a.go"},
		{"sha256sum", "sha256sum " + outside + "/a.go"},
		{"wc", "wc -l " + outside + "/a.go"},
		// The directory a command ENTERS is itself the read everything after it
		// depends on.
		{"cd then ls", "cd " + outside + " && ls"},
		{"cd then rg", "cd " + outside + " && rg -n x ."},
		{"cd then cksum", "cd " + outside + " && cksum a.go"},
		// A second file named by a flag rather than as a bare operand.
		{"grep -f", "grep -f " + outside + "/pat " + outside + "/a.go"},
		{"grep --file=", "grep --file=" + outside + "/pat " + outside + "/a.go"},
		{"rg --ignore-file", "rg --ignore-file " + outside + "/pat x " + outside},
		{"sort --files0-from", "sort --files0-from=" + outside + "/list"},
		{"wc --files0-from", "wc --files0-from " + outside + "/list"},
		{"diff --from-file", "diff --from-file=" + outside + "/a.go " + outside + "/notes"},
		{"file -m", "file -m " + outside + "/magic " + outside + "/a.go"},
		{"date -r", "date -r " + outside + "/a.go"},
		// git reading outside the repository is exactly what --no-index does.
		{"git diff --no-index", "git diff --no-index " + outside + "/a.go " + outside + "/notes"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Each case is its own scenario: a refusal recorded by the previous
			// one would block here without asking, which is correct behaviour
			// but not what this test is about.
			grantstest.Install(t)
			asked := 0
			reviewer := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
			scope := BoundaryGate(BoundaryOpts{
				WorkspaceRoot: workspace,
				Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
					asked++
					return ApprovalAnswer{Answered: true} // deny: the question is the point
				},
			})
			perm := PermissionGate(permissions.New(permissions.Auto), GateOpts{
				Classifier: Chain{Inner: reviewer},
				Sandboxed:  func(string) bool { return true },
			})
			chained := ChainGates(scope, perm)
			dec := chained(context.Background(), agentcore.AgentToolCall{
				Name: "bash", Arguments: bashArgs(c.cmd),
			})

			recognized := readOnlyCommand(c.cmd)
			if asked == 0 && reviewer.calls == 0 {
				t.Fatalf("%q was neither asked about nor reviewed (recognized=%v).\n"+
					"The consent gate saw no path AND the fast path allowed it: the read reaches\n"+
					"the filesystem with no layer looking. Either scan the operand or stop\n"+
					"recognizing the command.", c.cmd, recognized)
			}
			// A denial of consent must stand: the gate blocks, and the reviewer
			// (a layer that would have run later) is never consulted.
			if asked > 0 && !dec.Block {
				t.Fatalf("%q: the consent question was denied but the call was not blocked (%+v)", c.cmd, dec)
			}
		})
	}
}

// TestInsideWorkspaceReadsAreNotQuestioned is the other half of the invariant:
// the regression above must not have been bought by asking about ordinary
// in-workspace reads, which are not a boundary question.
func TestInsideWorkspaceReadsAreNotQuestioned(t *testing.T) {
	workspace, _ := boundaryFixture(t)
	for _, cmd := range []string{
		"ls -la",
		"cat " + workspace + "/inside",
		"rg -n TODO .",
		"cksum " + workspace + "/inside",
		"cd " + workspace + " && ls",
		"grep -f " + filepath.Join(workspace, "inside") + " " + filepath.Join(workspace, "inside"),
	} {
		asked := 0
		scope := BoundaryGate(BoundaryOpts{
			WorkspaceRoot: workspace,
			Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
				asked++
				return ApprovalAnswer{Answered: true}
			},
		})
		if dec := scope(context.Background(), agentcore.AgentToolCall{
			Name: "bash", Arguments: bashArgs(cmd),
		}); dec != nil {
			t.Errorf("%q was gated (%+v), want silence for an in-workspace read", cmd, dec)
		}
		if asked != 0 {
			t.Errorf("%q asked %d times, want 0 for an in-workspace read", cmd, asked)
		}
	}
}

// TestRecognizedCommandsAreTheOnesTheGateSaw documents the relationship rather
// than asserting a count: for every recognized command in the table above, the
// operand scanner must report at least one candidate. It is the cheap structural
// version of the invariant, and it fails the moment a program joins the
// whitelist without joining the operand table.
func TestRecognizedCommandsAreTheOnesTheGateSaw(t *testing.T) {
	_, outside := boundaryFixture(t)
	for _, cmd := range []string{
		"cksum " + outside + "/a.go",
		"base32 " + outside + "/a.go",
		"du -sh " + outside,
		"readlink " + outside + "/a.go",
		"realpath " + outside + "/a.go",
		"cd " + outside + " && ls",
		"grep -f " + outside + "/pat " + outside + "/a.go",
	} {
		if !readOnlyCommand(cmd) {
			t.Errorf("%q is not recognized; this test's premise (recognized AND scanned) is gone", cmd)
			continue
		}
		if got := bashPathCandidates(cmd); len(got) == 0 {
			t.Errorf("%q is recognized as read-only but the scanner reports no path operand; "+
				"the consent gate will stay silent while the reviewer is skipped", cmd)
		} else if !strings.Contains(strings.Join(got, " "), outside) {
			t.Errorf("%q candidates = %v, want one under %q", cmd, got, outside)
		}
	}
}

// TestScannedButNotRecognizedStaysReviewed pins the fail-safe direction for a
// command whose flags name a file while its bare operands are dangerous: `date
// -r FILE` reads a file, but `date 010112002026` (BSD) sets the clock with no
// flag at all, so date-with-operands is left to the reviewer. The gate still
// asks about the file — that is what fileArgFlags is for — and the reviewer
// still runs, which is the conservative half of the pair.
func TestScannedButNotRecognizedStaysReviewed(t *testing.T) {
	workspace, outside := boundaryFixture(t)
	cmd := "date -r " + outside + "/a.go"
	if readOnlyCommand(cmd) {
		t.Fatalf("%q is recognized; date-with-operands must stay with the reviewer "+
			"(BSD `date MMDDhhmm` sets the clock without a flag)", cmd)
	}
	if got := bashPathCandidates(cmd); len(got) == 0 {
		t.Errorf("%q yields no candidates; the file named by -r must still be scanned so the "+
			"consent gate asks about it", cmd)
	}
	asked := 0
	reviewer := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	scope := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	perm := PermissionGate(permissions.New(permissions.Auto), GateOpts{
		Classifier: Chain{Inner: reviewer},
		Sandboxed:  func(string) bool { return true },
	})
	ChainGates(scope, perm)(context.Background(), agentcore.AgentToolCall{
		Name: "bash", Arguments: bashArgs(cmd),
	})
	if asked == 0 {
		t.Errorf("%q: the boundary gate stayed silent on a path named by -r", cmd)
	}
}
