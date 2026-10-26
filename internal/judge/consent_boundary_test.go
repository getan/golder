package judge

// Boundary regressions for the live consent gate. These are the properties the
// old BoundaryGate tests protected, expressed against the one gate that now
// owns the question: the path readers still see the same operands, the write
// side still differs from the read side, and a refusal or a standing grant is
// still a decision the next call obeys.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

func boundaryOpts(cwd string) ConsentOpts {
	return ConsentOpts{Cwd: cwd}
}

func boundaryNeedFor(t *testing.T, opts ConsentOpts, call agentcore.AgentToolCall) []Need {
	t.Helper()
	return boundaryNeeds(call, opts, permissions.Auto, "en")
}

func TestConsentBoundaryDevNullRedirectNeedsNoGrant(t *testing.T) {
	workspace, _, target := readFixture(t)
	opts := boundaryOpts(workspace)
	for _, command := range []string{
		`ls . 2>/dev/null || echo "no partial dir"`,
		`printf hi >/dev/null`,
		`printf hi >>"/dev/null"`,
		`cat </dev/null`,
	} {
		// Both calls must be silent: no first-call failure followed by a
		// session grant that happens to make the next call work.
		for range 2 {
			if needs := boundaryNeedFor(t, opts, bashCall(command)); len(needs) != 0 {
				t.Errorf("%q needs = %+v, want no boundary grant", command, needs)
			}
		}
	}
	needs := boundaryNeedFor(t, opts, bashCall("cat "+target+" 2>/dev/null"))
	if len(needs) != 1 || needs[0].Cap != CapReadOutside || needs[0].Path != target {
		t.Fatalf("real outside read must still need its grant: %+v", needs)
	}
}

// TestConsentBoundaryReadNeeds pins the silent half: in-workspace, system and
// advertised roots are not questions; an outside file is one, scoped to its
// containing directory; credential material is never offered at all.
func TestConsentBoundaryReadNeeds(t *testing.T) {
	workspace, outside, target := readFixture(t)
	opts := boundaryOpts(workspace)

	for _, tc := range []struct {
		name string
		call agentcore.AgentToolCall
	}{
		{"inside", readCall("read", "a.go")},
		{"inside subdir", readCall("read", "sub/b.go")},
		{"system", readCall("read", "/usr/bin/env")},
		{"credential", readCall("read", "~/.ssh/id_rsa")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if needs := boundaryNeedFor(t, opts, tc.call); len(needs) != 0 {
				t.Fatalf("needs = %+v, want none", needs)
			}
		})
	}

	needs := boundaryNeedFor(t, opts, readCall("read", target))
	if len(needs) != 1 || needs[0].Cap != CapReadOutside {
		t.Fatalf("outside read needs = %+v, want one read need", needs)
	}
	if needs[0].Path != target || needs[0].Dir != outside {
		t.Errorf("outside read need = %+v, want path %q dir %q", needs[0], target, outside)
	}

	// An advertised root is the skills-directory case: it is not a question.
	advertised := boundaryOpts(workspace)
	advertised.ExtraRoots = []string{outside}
	if needs := boundaryNeedFor(t, advertised, readCall("read", target)); len(needs) != 0 {
		t.Fatalf("advertised root needs = %+v, want none", needs)
	}

	// The floor is what makes passing a credential path through safe.
	if _, denied := staticDeny("read", readCall("read", "~/.ssh/id_rsa").Arguments); !denied {
		t.Fatal("the static floor must deny a credential path")
	}
}

// TestConsentBoundaryWrites pins the write side: a shell redirect or write verb
// outside the workspace is a CapWriteOutside need, while an in-workspace write
// stays silent. The side is read from the command text, not from the tool's
// table entry.
func TestConsentBoundaryWrites(t *testing.T) {
	workspace, outside, target := readFixture(t)
	opts := boundaryOpts(workspace)
	out := filepath.Join(outside, "out.txt")

	writeNeed := func(cmd string) []Need {
		t.Helper()
		args, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatal(err)
		}
		return boundaryNeedFor(t, opts, agentcore.AgentToolCall{Name: "bash", Arguments: args})
	}

	for _, cmd := range []string{
		"echo hi > " + out,
		"rm -f " + out,
		"mkdir -p " + out,
	} {
		needs := writeNeed(cmd)
		if len(needs) != 1 || needs[0].Cap != CapWriteOutside {
			t.Errorf("%q needs = %+v, want one write need", cmd, needs)
			continue
		}
		if needs[0].Path != out && needs[0].Path != target {
			t.Errorf("%q path = %q, want the named operand", cmd, needs[0].Path)
		}
	}

	inside := filepath.Join(workspace, "out.txt")
	if needs := writeNeed("echo hi > " + inside); len(needs) != 0 {
		t.Errorf("in-workspace redirect needs = %+v, want none", needs)
	}
}

// TestConsentBoundaryOperandForms covers the reader edge cases the old gate
// tests pinned: tilde expansion, the list form, several operands, and program
// text that must not be scanned for paths.
func TestConsentBoundaryOperandForms(t *testing.T) {
	workspace, outside, target := readFixture(t)
	opts := boundaryOpts(workspace)

	t.Run("tilde", func(t *testing.T) {
		needs := boundaryNeedFor(t, opts, bashCall("cat ~/outside/a.go"))
		if len(needs) != 1 || needs[0].Path != target {
			t.Fatalf("tilde needs = %+v, want %q", needs, target)
		}
	})

	t.Run("path list", func(t *testing.T) {
		args, err := json.Marshal(map[string]any{"pattern": "x", "path": []string{outside}})
		if err != nil {
			t.Fatal(err)
		}
		needs := boundaryNeedFor(t, opts, agentcore.AgentToolCall{Name: "grep", Arguments: args})
		if len(needs) != 1 || needs[0].Path != outside || needs[0].Dir != outside {
			t.Fatalf("list needs = %+v, want the outside directory", needs)
		}
	})

	t.Run("several operands", func(t *testing.T) {
		sub := filepath.Join(outside, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		needs := boundaryNeedFor(t, opts, bashCall("ls "+outside+" "+sub))
		if len(needs) != 2 {
			t.Fatalf("needs = %+v, want one per outside operand", needs)
		}
		got := map[string]bool{}
		for _, n := range needs {
			got[n.Path] = true
		}
		if !got[outside] || !got[sub] {
			t.Errorf("needs = %+v, want both %q and %q", needs, outside, sub)
		}
	})

	t.Run("program text", func(t *testing.T) {
		if needs := boundaryNeedFor(t, opts, bashCall(`python3 -c 'print("/outside/x")'`)); len(needs) != 0 {
			t.Fatalf("program text needs = %+v, want none", needs)
		}
	})
}

// TestConsentBoundaryGrantsAndRefusals pins the two table answers: a standing
// read grant silences the need, and an explicit refusal blocks the call without
// asking again. The refusal is scoped to its exact path.
func TestConsentBoundaryGrantsAndRefusals(t *testing.T) {
	workspace, outside, target := readFixture(t)
	opts := boundaryOpts(workspace)

	if _, err := grants.AddReadRoot(outside, grants.Session, "test"); err != nil {
		t.Fatal(err)
	}
	if needs := boundaryNeedFor(t, opts, readCall("read", target)); len(needs) != 0 {
		t.Fatalf("granted read needs = %+v, want none", needs)
	}

	// A refusal is an answer: the next call is blocked without a dialog.
	refused := filepath.Join(outside, "refused.txt")
	if _, err := grants.Add(grants.Grant{
		Path: refused, Deny: grants.Read, Scope: grants.Exact,
		Lifetime: grants.Session, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	dec := boundaryBlocked(readCall("read", refused), opts)
	if dec == nil || !dec.Block {
		t.Fatalf("refused path = %+v, want a block", dec)
	}
	msg := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(msg, "refused earlier") || !strings.Contains(msg, "/boundary readable add") {
		t.Errorf("refusal = %q, want the reason and the way back", msg)
	}
	// The sibling was never answered, so it is still a question.
	if dec := boundaryBlocked(readCall("read", filepath.Join(outside, "sibling.txt")), opts); dec != nil {
		t.Errorf("a refusal for one path silenced its sibling: %+v", dec)
	}
}

// TestConsentBoundaryCloneDestination keeps the clone regression: a clone into a
// sibling of the workspace is one write need for the destination itself, not a
// read and not the tree holding every sibling.
func TestConsentBoundaryCloneDestination(t *testing.T) {
	workspace, _, _ := readFixture(t)
	opts := boundaryOpts(workspace)
	args, err := json.Marshal(map[string]string{"command": cloneBrowserUse})
	if err != nil {
		t.Fatal(err)
	}
	needs := boundaryNeedFor(t, opts, agentcore.AgentToolCall{Name: "bash", Arguments: args})
	if len(needs) != 1 || needs[0].Cap != CapWriteOutside {
		t.Fatalf("clone needs = %+v, want one write need", needs)
	}
	want := filepath.Clean(filepath.Join(filepath.Dir(workspace), "browser-use"))
	if needs[0].Path != want || needs[0].Dir != want {
		t.Errorf("clone need = %+v, want destination %q", needs[0], want)
	}
	if needs[0].Dir == filepath.Dir(workspace) {
		t.Error("the clone destination widened to the tree holding the workspace")
	}
}

// TestConsentGateAppliesBoundaryAnswer drives the question end to end: ask mode
// puts one request with the read need, and the answer applies the one-call read
// grant the tool consumes.
func TestConsentGateAppliesBoundaryAnswer(t *testing.T) {
	workspace, _, target := readFixture(t)
	asked := 0
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: &stubClassifier{v: Verdict{Level: Allow}},
		Cwd:        workspace,
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			asked++
			if len(req.Needs) != 1 || req.Needs[0].Cap != CapReadOutside || req.Needs[0].Path != target {
				t.Fatalf("request needs = %+v, want the outside read", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs}
		},
	})
	dec := gate(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Fatalf("asked = %d, want one question", asked)
	}
	if dec == nil || dec.Block || dec.ReadGrant != target {
		t.Fatalf("answer = %+v, want the one-call read grant for %q", dec, target)
	}
}
