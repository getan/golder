package judge

// Tests for the write side of the boundary question. A patch reaching outside
// the workspace used to have no consent path at all — the tool refused it, the
// gate never asked, and only the reviewer saw it — so these pin the shape that
// replaced that: the same question as a read, the same three answers, and a
// grant that says it is about writing.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
)

// TestBoundaryGateAsksAboutAWrite pins the question itself, and that the
// request names the side: the dialog's offers differ ("allow reading /x" is not
// "allow writing /x"), so a request that did not say which would put read rows
// under a write question.
func TestBoundaryGateAsksAboutAWrite(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	var got ApprovalRequest
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			got = req
			return ApprovalAnswer{Answered: true} // deny: the question is what matters
		},
	})
	dec := gate(context.Background(), patchCall(filepath.Join(outside, "notes.md")))
	if dec == nil || !dec.Block {
		t.Fatalf("an out-of-workspace patch must be gated, got %+v", dec)
	}
	if got.Kind != ApprovalBoundary {
		t.Errorf("kind = %q, want %q", got.Kind, ApprovalBoundary)
	}
	if !got.BoundaryWrite {
		t.Error("a write question must say it is about writing, or the dialog offers read rows")
	}
	// The refusal the model reads names the side too: it is guidance for the next
	// attempt, and "the read was not approved" about a write misleads.
	if txt := agentcore.ContentToText(*dec.Content); !strings.Contains(txt, "the write was not approved") {
		t.Errorf("the block message should name the write: %q", txt)
	}
}

// TestBoundaryGateSilentForAnInWorkspacePatch is the common case, and the one
// the whole change must not disturb: an ordinary patch is not a question.
func TestBoundaryGateSilentForAnInWorkspacePatch(t *testing.T) {
	workspace, _, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), patchCall(filepath.Join(workspace, "a.go"))); dec != nil {
		t.Errorf("an in-workspace patch must not be gated, got %+v", dec)
	}
	if asked != 0 {
		t.Errorf("no question should have been asked, got %d", asked)
	}
}

// TestBoundaryGateWriteOnceReturnsAWriteGrant covers the narrowest answer: the
// decision carries a one-call WRITE grant, and no read grant — the two are
// different permissions, and handing out the wrong one would either fail the
// write the user approved or authorize something they did not.
func TestBoundaryGateWriteOnceReturnsAWriteGrant(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	})
	dec := gate(context.Background(), patchCall(filepath.Join(outside, "notes.md")))
	if dec == nil || dec.Block {
		t.Fatalf("an approved write must not block, got %+v", dec)
	}
	if dec.WriteGrant == "" {
		t.Fatalf("the one-write answer must carry a write grant, got %+v", dec)
	}
	if dec.ReadGrant != "" {
		t.Errorf("a write answer must not hand out a read grant, got %q", dec.ReadGrant)
	}
}

// TestBoundaryGateWriteDirectoryRegistersThroughGrantWritable pins where a
// directory answer lands: the same callback the escalation dialog uses, which
// registers the sandbox root and the table row together. The gate must not
// decide on its own what a writable directory means.
func TestBoundaryGateWriteDirectoryRegistersThroughGrantWritable(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	var gotDir string
	var gotPersist bool
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			return ApprovalAnswer{Approve: true, Answered: true, WriteRoot: outside, Persisted: true}
		},
		GrantWritable: func(root string, persist bool) (bool, string) {
			gotDir, gotPersist = root, persist
			return true, ""
		},
	})
	if dec := gate(context.Background(), patchCall(filepath.Join(outside, "notes.md"))); dec != nil {
		t.Fatalf("a registered directory grant needs no one-call decision, got %+v", dec)
	}
	if gotDir != outside {
		t.Errorf("GrantWritable got %q, want %q", gotDir, outside)
	}
	if !gotPersist {
		t.Error("the remembered answer must reach GrantWritable as persist=true")
	}
}

// TestBoundaryWriteQuestionIsNotSilencedByAReadGrant is the capability
// separation seen from the gate: a directory allowed for reading is not thereby
// writable, so the write question still gets asked — and a write grant does
// settle it.
func TestBoundaryWriteQuestionIsNotSilencedByAReadGrant(t *testing.T) {
	workspace, outside, _, store := authFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	target := filepath.Join(outside, "notes.md")

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting read: %v", err)
	}
	gate(context.Background(), patchCall(target))
	if asked != 1 {
		t.Errorf("a read grant must not settle the write question (asked %d times)", asked)
	}

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Write, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting write: %v", err)
	}
	gate(context.Background(), patchCall(target))
	if asked != 1 {
		t.Errorf("a write grant must settle the question (asked %d times)", asked)
	}
}

// TestBoundaryWriteHonoursTheWritableRegistry covers the other silencer: a
// directory the sandbox already writes — seeded from config.toml or granted by
// an earlier escalation answer — is not asked about again.
func TestBoundaryWriteHonoursTheWritableRegistry(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		WritableAt:    func(p string) bool { return true },
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), patchCall(filepath.Join(outside, "notes.md"))); dec != nil {
		t.Errorf("an already-writable path must not be gated, got %+v", dec)
	}
	if asked != 0 {
		t.Errorf("no question should have been asked, got %d", asked)
	}
}
