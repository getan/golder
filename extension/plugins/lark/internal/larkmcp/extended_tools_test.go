package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExtendedCatalogReadWriteAndBounds(t *testing.T) {
	cases := []struct {
		name  string
		write bool
	}{
		{"calendar_read", false}, {"calendar_write", true},
		{"task_read", false}, {"task_write", true},
		{"wiki_read", false}, {"wiki_write", true},
	}
	for _, tc := range cases {
		d := catalogDefinition(t, tc.name)
		if d.Write != tc.write {
			t.Fatalf("%s Write = %t, want %t", tc.name, d.Write, tc.write)
		}
	}
	if got := optionInt(t, catalogDefinition(t, "calendar_read"), "page_size", "maximum"); got != 30 {
		t.Fatalf("calendar page_size maximum = %d, want 30", got)
	}
	if got := optionInt(t, catalogDefinition(t, "task_read"), "page_limit", "maximum"); got != 40 {
		t.Fatalf("task page_limit maximum = %d, want 40", got)
	}
	if got := optionInt(t, catalogDefinition(t, "wiki_read"), "page_size", "maximum"); got != 50 {
		t.Fatalf("wiki page_size maximum = %d, want 50", got)
	}
}

func TestExtendedReadCommandsAndTaskPendingDefault(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	if r := p.Call(context.Background(), "calendar_read", json.RawMessage(`{"operation":"agenda","options":{"start":"2026-11-13","end":"2026-11-14"}}`)); !r.OK {
		t.Fatalf("calendar agenda failed: %+v", r)
	}
	if r := p.Call(context.Background(), "task_read", json.RawMessage(`{"operation":"mine","options":{}}`)); !r.OK {
		t.Fatalf("task mine failed: %+v", r)
	}
	if r := p.Call(context.Background(), "wiki_read", json.RawMessage(`{"operation":"nodes","options":{"space_id":"space"}}`)); !r.OK {
		t.Fatalf("wiki nodes failed: %+v", r)
	}
	if r := p.Call(context.Background(), "calendar_read", json.RawMessage(`{"operation":"search","options":{"start":"2026-11-13","end":"2026-11-14"}}`)); !r.OK {
		t.Fatalf("calendar search without keyword failed: %+v", r)
	}
	if len(f.calls) != 4 ||
		!strings.Contains(f.calls[0], "calendar +agenda --end=2026-11-14 --start=2026-11-13 --as=user") ||
		!strings.Contains(f.calls[1], "task +get-my-tasks --complete=false --as=user") ||
		!strings.Contains(f.calls[2], "wiki +node-list --space-id=space --as=user") ||
		!strings.Contains(f.calls[3], "calendar +search-event --end=2026-11-14 --start=2026-11-13 --as=user") {
		t.Fatalf("extended read commands = %v", f.calls)
	}
}

func TestExtendedWritesUseHostApprovalAndHighRiskYes(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	if r := p.Call(approved(), "calendar_write", json.RawMessage(`{"operation":"delete","options":{"event_id":"event"},"confirm":true}`)); !r.OK {
		t.Fatalf("calendar delete failed: %+v", r)
	}
	if r := p.Call(approved(), "wiki_write", json.RawMessage(`{"operation":"node_delete","options":{"node_token":"node"},"confirm":true}`)); !r.OK {
		t.Fatalf("wiki node delete failed: %+v", r)
	}
	if r := p.Call(approved(), "task_write", json.RawMessage(`{"operation":"complete","options":{"task_id":"task"},"confirm":true}`)); !r.OK {
		t.Fatalf("task complete failed: %+v", r)
	}
	if len(f.calls) != 3 ||
		!strings.Contains(f.calls[0], "calendar +delete --event-id=event --yes --as=user") ||
		!strings.Contains(f.calls[1], "wiki +node-delete --node-token=node --yes --as=user") ||
		!strings.Contains(f.calls[2], "task +complete --task-id=task --as=user") {
		t.Fatalf("extended write commands = %v", f.calls)
	}
	if r := p.Call(context.Background(), "calendar_write", json.RawMessage(`{"operation":"delete","options":{"event_id":"event"},"confirm":true}`)); r.OK || r.Error == nil || r.Error.Code != "OPERATOR_CONFIRMATION_REQUIRED" || len(f.calls) != 3 {
		t.Fatalf("unapproved write reached runner: %+v calls=%v", r, f.calls)
	}
}

func TestExtendedWriteValidation(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"calendar pair", `{"operation":"update","options":{"event_id":"e","start":"2026-11-13"},"confirm":true}`},
		{"wiki shortcut", `{"operation":"node_create","options":{"space_id":"s","title":"t","node_type":"shortcut"},"confirm":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "calendar_write"
			if tc.name == "wiki shortcut" {
				name = "wiki_write"
			}
			r := p.Call(approved(), name, json.RawMessage(tc.raw))
			if r.OK || r.Error == nil || r.Error.Code != "INVALID_ARGUMENTS" || len(f.calls) != 0 {
				t.Fatalf("invalid write reached runner: %+v calls=%v", r, f.calls)
			}
		})
	}
}
