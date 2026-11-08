package appsession

import (
	"testing"

	"github.com/getan/golder/internal/judge"
)

func TestNoteVisibilityMatchesTUI(t *testing.T) {
	for _, tc := range []struct {
		name, debug, mode string
		kind              judge.NoteKind
		visible           bool
	}{
		{"automatic approval", "", "auto", judge.NoteApproved, false},
		{"manual approval", "", "ask", judge.NoteApproved, false},
		{"debug approval", "1", "auto", judge.NoteApproved, true},
		{"debug true approval", "true", "ask", judge.NoteApproved, true},
		{"sandboxed", "", "auto", judge.NoteSandboxed, true},
		{"escaped", "", "auto", judge.NoteEscaped, true},
		{"denied", "", "auto", judge.NoteDenied, true},
		{"unavailable", "", "auto", judge.NoteUnavailable, true},
		{"blocked", "", "auto", judge.NoteBlockedNoPrompt, true},
		{"read only", "", "auto", judge.NoteReadOnly, true},
		{"boundary granted", "", "ask", judge.NoteBoundaryGranted, true},
		{"boundary denied", "", "ask", judge.NoteBoundaryDenied, true},
		{"boundary no prompt", "", "ask", judge.NoteBoundaryNoPrompt, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOLDER_DEBUG", tc.debug)
			c := New(Options{})
			c.addNote(judge.Note{Tool: "bash", Kind: tc.kind, Mode: tc.mode, Rationale: "test rationale"})
			items := c.Snapshot().Items
			if !tc.visible {
				if len(items) != 0 {
					t.Fatalf("hidden note in snapshot: %+v", items)
				}
				select {
				case ev := <-c.Events():
					t.Fatalf("hidden note emitted event: %+v", ev)
				default:
				}
				return
			}
			if len(items) != 1 || items[0].Kind != ItemNote || items[0].Note == nil || items[0].Note.Kind != int(tc.kind) {
				t.Fatalf("visible note missing from snapshot: %+v", items)
			}
			select {
			case ev := <-c.Events():
				if ev.Kind != EventItemAdded || ev.Item == nil || ev.Item.ID != items[0].ID {
					t.Fatalf("unexpected note event: %+v", ev)
				}
			default:
				t.Fatal("visible note did not emit an event")
			}
		})
	}
}
