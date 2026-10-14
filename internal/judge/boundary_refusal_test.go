package judge

// Tests for a refusal as a decision rather than a single blocked call.
//
// The gate used to forget a "no" the moment it was given: the same command
// asked the same question again on the next turn, and a session that read
// outside the workspace repeatedly turned into a wall of identical dialogs —
// which is how a prompt becomes something people dismiss without reading. A
// refusal is now recorded (the session layer, exact path, so Esc means "not
// now" rather than "never") and the next call is blocked without asking.

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
)

// TestBoundaryGateDoesNotReAskARefusedPath is the property: one refusal, one
// question. The second call is still blocked — the answer stands — but the user
// is not asked twice for it.
func TestBoundaryGateDoesNotReAskARefusedPath(t *testing.T) {
	workspace, _, target, _ := authFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true} // an explicit no
		},
	})

	if dec := gate(context.Background(), readCall("read", target)); dec == nil || !dec.Block {
		t.Fatalf("a refused read must be blocked, got %+v", dec)
	}
	if asked != 1 {
		t.Fatalf("the first call asked %d times, want 1", asked)
	}

	dec := gate(context.Background(), readCall("read", target))
	if dec == nil || !dec.Block {
		t.Fatalf("the refused path must stay blocked, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("the same question was asked again (%d times): a refusal is an answer", asked)
	}
	// The message must say WHY it was blocked without a dialog — a refusal that
	// reads like a fresh denial leaves the model retrying and the person
	// wondering what just happened — and name how to change the answer.
	msg := agentcore.ContentToText(*dec.Content)
	for _, want := range []string{"refused earlier", "/permissions readable add"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the second block should mention %q: %q", want, msg)
		}
	}
}

// TestBoundaryGateRefusalIsScopedToItsPath keeps the refusal the size of the
// answer: the dialog named a file, so its sibling is still a question. Recording
// a refusal for the whole directory would turn one "not this file" into a
// closed door.
func TestBoundaryGateRefusalIsScopedToItsPath(t *testing.T) {
	workspace, _, target, _ := authFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	gate(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Fatalf("the first call asked %d times, want 1", asked)
	}

	sibling := readCall("read", target[:len(target)-len("a.go")]+"b.go")
	if dec := gate(context.Background(), sibling); dec == nil || !dec.Block {
		t.Fatalf("the sibling is an unanswered question, not a refusal: %+v", dec)
	}
	if asked != 2 {
		t.Errorf("a refusal for one path must not silence another (asked %d times, want 2)", asked)
	}
}

// TestBoundaryGateNoPromptDoesNotRecordARefusal is the distinction the record
// depends on: "no prompt available" is a failure to ASK, not an answer. If it
// were recorded as a refusal, a headless run's silence would become a decision
// the user never made — and the interactive run that followed would refuse a
// path nobody had ever been asked about.
func TestBoundaryGateNoPromptDoesNotRecordARefusal(t *testing.T) {
	workspace, _, target, store := authFixture(t)
	headless := BoundaryGate(BoundaryOpts{WorkspaceRoot: workspace}) // no Confirm, no In/Out
	if dec := headless(context.Background(), readCall("read", target)); dec == nil || !dec.Block {
		t.Fatalf("with nobody to ask the read must fail closed, got %+v", dec)
	}
	if v := store.Check(target, grants.Read); v.Decision == grants.Denied {
		t.Error("a run with no prompt recorded a refusal; the user was never asked")
	}

	// So the interactive run that follows still asks.
	asked := 0
	interactive := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	interactive(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Errorf("the question was not put after a promptless failure (asked %d times)", asked)
	}
}

// TestBoundaryGateGrantLiftsAnEarlierRefusal pins the way out, and that the
// command the block names is the one that works. The refusal is a row for the
// EXACT path, so the exact path is what must be granted: the table answers
// most-specific-first, so a grant for the parent directory does not lift a
// refusal recorded for one file inside it, and naming the parent would send the
// person to a command that changes nothing.
func TestBoundaryGateGrantLiftsAnEarlierRefusal(t *testing.T) {
	workspace, _, target, store := authFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	gate(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Fatalf("the first call asked %d times, want 1", asked)
	}

	if _, err := store.Add(grants.Grant{
		Path: target, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting %s: %v", target, err)
	}
	if dec := gate(context.Background(), readCall("read", target)); dec != nil {
		t.Errorf("a granted path must run, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("a granted path must not ask (asked %d times)", asked)
	}
}

// TestBoundaryGateDirectoryGrantDoesNotLiftAChildRefusal records the limit of
// the answer above, because it is the sort of thing that looks like a bug when
// met in the wild: granting the directory does not reopen a file that was
// refused by name. The table resolves most-specific-first, and that is what
// keeps "not this file" from being overridden by a broader grant made earlier
// — the person who refuses one file has not thereby refused its siblings, and
// the person who grants a directory has not thereby answered for that file.
func TestBoundaryGateDirectoryGrantDoesNotLiftAChildRefusal(t *testing.T) {
	workspace, outside, target, store := authFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	gate(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Fatalf("the first call asked %d times, want 1", asked)
	}

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting %s: %v", outside, err)
	}
	dec := gate(context.Background(), readCall("read", target))
	if dec == nil || !dec.Block {
		t.Fatalf("the exact refusal still stands, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("the question must not be re-asked (asked %d times)", asked)
	}
	if msg := agentcore.ContentToText(*dec.Content); !strings.Contains(msg, "/permissions readable add "+target) {
		t.Errorf("the block should name the exact path that lifts it: %q", msg)
	}
}
