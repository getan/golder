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

			if asked == 0 && reviewer.calls == 0 {
				t.Fatalf("%q was neither asked about nor reviewed; the boundary gate must see file operands", c.cmd)
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

// TestOutsideFileOperandsReachTheGate tests extraction for representative shell
// readers. The OS sandbox is the enforcement if an unknown reader is absent
// from this consent-oriented list; no separate hand-written read-only parser
// decides whether to isolate a command anymore.
func TestOutsideFileOperandsReachTheGate(t *testing.T) {
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
		if got := commandPaths(cmd); len(got) == 0 {
			t.Errorf("%q reports no path operand; the consent gate cannot ask about it", cmd)
		} else if !strings.Contains(strings.Join(got, " "), outside) {
			t.Errorf("%q candidates = %v, want one under %q", cmd, got, outside)
		}
	}
}

// TestFileFlagReadIsAskedAndReviewed: a flag can name a file even when the
// command has other effects; consent and authorization are separate questions.
func TestFileFlagReadIsAskedAndReviewed(t *testing.T) {
	workspace, outside := boundaryFixture(t)
	cmd := "date -r " + outside + "/a.go"
	if got := commandPaths(cmd); len(got) == 0 {
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

// TestProgramTextIsNotScannedForPaths pins the fix for a consent dialog that
// asked about a divisor.
//
// The reported command was a python program printing token arithmetic, and the
// gate asked the user to allow reading `/60` — a run the scanner pulled out of
// the program text because a `)` happened to precede the division. The path was
// never a path: `(n*med)/60` divides.
//
// The fix is a predicate change, not a case: an interpreter's program is read
// for path-shaped runs, and a run counts only if it sits inside a quoted string
// AND exists on disk — and a shell program (`sh -c`) is not scanned at all but
// parsed as the command line it is. This test holds both halves: the reported
// shape raises nothing, and the true positive beside it still raises the
// question.
func TestProgramTextIsNotScannedForPaths(t *testing.T) {
	workspace := t.TempDir()

	// The reported shape, verbatim in structure: arithmetic, division, and a
	// newline-separated program body.
	reported := "python3 -c \"\n" +
		"n=776; med=0.776\n" +
		"print('耗时: %.0f 秒' % (n*med/60))\n" +
		"print('节省: %.1f 分钟' % ((n*2.16 - n*med)/60))\n" +
		"\""
	if got := commandPaths(reported); len(got) != 0 {
		t.Errorf("commandPaths = %v, want none: `/60` is a divisor, not a path", got)
	}

	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true} // deny
		},
	})
	_ = gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: bashArgs(reported)})
	if asked != 0 {
		t.Errorf("the gate asked %d question(s) about a program with no path in it", asked)
	}

	// The true positive: a shell program IS a command line, so its read is seen.
	shellProgram := "sh -c 'cat /etc/hosts'"
	if got := commandPaths(shellProgram); len(got) != 1 || got[0] != "/etc/hosts" {
		t.Errorf("commandPaths(%q) = %v, want [/etc/hosts]", shellProgram, got)
	}
}

// TestPythonGlobDoesNotAskForFilesystemRoot reproduces the prompt that named
// "/" for a Python program whose only slash was inside the string "/*.jsonl".
// Breaking a lexical path run at '*' leaves the existing root "/"; the
// non-shell program scanner must instead check the entire string before it
// can offer any path to the boundary gate.
func TestPythonGlobDoesNotAskForFilesystemRoot(t *testing.T) {
	workspace, outside := boundaryFixture(t)
	cmd := "cd " + workspace + ` && python3 -c 'import json,glob,os;
p=os.path.expanduser("~/.golder/sessions"); files=glob.glob(p+"/*.jsonl");
print("sessions",len(files))'`
	if got := commandPaths(cmd); len(got) != 1 || got[0] != workspace {
		t.Fatalf("commandPaths = %v, want only the workspace cd (not /)", got)
	}
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: bashArgs(cmd)}); dec != nil || asked != 0 {
		t.Fatalf("glob-only command prompted %d times; decision %+v", asked, dec)
	}

	// Rejecting the bogus prefix must not hide an existing external file opened
	// by the same interpreter: the guard against a false positive cannot erase
	// the true positive the consent gate exists to catch.
	read := `python3 -c 'import glob; glob.glob("/*.jsonl"); open("` + outside + `/a.go")'`
	if got := commandPaths(read); len(got) != 1 || got[0] != outside+"/a.go" {
		t.Fatalf("commandPaths(real read) = %v, want the existing outside file", got)
	}
	if dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: bashArgs(read)}); dec == nil || !dec.Block || asked != 1 {
		t.Errorf("real outside read was not asked and denied: asked=%d decision=%+v", asked, dec)
	}
}
