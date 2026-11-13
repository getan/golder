package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeleteSheetTargetsVerifiedWorkbookAndReadsBack(t *testing.T) {
	for _, tc := range []struct {
		name, before, after, want string
		writes                    int
		preview                   bool
	}{
		{"verified", `{"sheets":[{"sheet_id":"s1"},{"sheet_id":"s2"}]}`, `{"sheets":[{"sheet_id":"s2"}]}`, "verified_absent", 1, false},
		{"still present", `{"sheets":[{"sheet_id":"s1"},{"sheet_id":"s2"}]}`, `{"sheets":[{"sheet_id":"s1"},{"sheet_id":"s2"}]}`, "unverified", 1, false},
		{"unknown readback", `{"sheets":[{"sheet_id":"s1"}]}`, `{"items":[]}`, "unverified", 1, false},
		{"wrong workbook", `{"sheets":[{"sheet_id":"s2"}]}`, "", "", 0, false},
		{"unknown workbook", `{"items":[]}`, "", "", 0, false},
		{"preview", `{"sheets":[{"sheet_id":"s1"}]}`, "", "preview", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, writes := 0, 0
			f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
				line := strings.Join(args, " ")
				if strings.Contains(line, "+workbook-info") {
					reads++
					value := tc.before
					if reads > 1 {
						value = tc.after
					}
					return Output{Stdout: []byte(`{"ok":true,"identity":"user","data":` + value + `}`)}, nil
				}
				if strings.Contains(line, "+sheet-delete") {
					writes++
					if !strings.Contains(line, "--sheet-id=s1") || !strings.Contains(line, "--spreadsheet-token=book") || !strings.Contains(line, "--yes") {
						t.Fatalf("incorrect delete target: %s", line)
					}
					return Output{Stdout: []byte(`{"ok":true,"identity":"user","data":{"receipt":"ok"}}`)}, nil
				}
				t.Fatalf("unexpected command: %s", line)
				return Output{}, nil
			}}
			p := New(Config{}, f)
			raw, _ := json.Marshal(map[string]any{"operation": "delete", "options": map[string]any{"spreadsheet_token": "book", "sheet_id": "s1", "dry_run": tc.preview}, "confirm": true})
			if r := p.Call(context.Background(), "sheet_structure", raw); r.OK || len(f.calls) != 0 {
				t.Fatal("unapproved deletion dispatched")
			}
			r := p.Call(approved(), "sheet_structure", raw)
			if writes != tc.writes || r.Verification != tc.want || (tc.writes == 0 && (r.OK || r.Error == nil || r.Error.Code != "SHEET_NOT_RESOLVED")) {
				t.Fatalf("result=%+v writes=%d reads=%d calls=%v", r, writes, reads, f.calls)
			}
			if tc.preview && (r.ExecutionState != "not_executed" || reads != 1) {
				t.Fatalf("preview performed readback or reported execution: %+v calls=%v", r, f.calls)
			}
		})
	}
}

func TestCommonDomainWrappers(t *testing.T) {
	for _, tc := range []struct{ name, raw, command string }{
		{"drive_write", `{"operation":"copy","options":{"url":"https://example.feishu.cn/sheets/token","name":"copy","folder_token":"my_space"},"confirm":true}`, "drive +copy"},
		{"drive_write", `{"operation":"create_folder","options":{"name":"new"},"confirm":true}`, "drive +create-folder"},
		{"task_write", `{"operation":"reopen","options":{"task_id":"task"},"confirm":true}`, "task +reopen"},
		{"task_write", `{"operation":"assign","options":{"task_id":"task","add":"ou_person"},"confirm":true}`, "task +assign"},
		{"calendar_write", `{"operation":"rsvp","options":{"event_id":"event","rsvp_status":"accept"},"confirm":true}`, "calendar +rsvp"},
		{"calendar_read", `{"operation":"freebusy","options":{"user_id":"ou_person","type":"free"}}`, "calendar +freebusy"},
		{"wiki_write", `{"operation":"node_copy","options":{"space_id":"space","node_token":"node","target_space_id":"other"},"confirm":true}`, "wiki +node-copy"},
		{"sheet_read", `{"operation":"search","options":{"spreadsheet_token":"book","sheet_id":"s1","find":"text"}}`, "sheets +cells-search"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			f := &fakeRunner{}
			r := New(Config{}, f).Call(approved(), tc.name, json.RawMessage(tc.raw))
			if !r.OK || len(f.calls) != 1 || !strings.Contains(f.calls[0], tc.command) {
				t.Fatalf("result=%+v calls=%v", r, f.calls)
			}
		})
	}
}

func TestSheetWriteOperationsRespectPreviewAndRisk(t *testing.T) {
	for _, tc := range []struct{ operation, extra, command string }{
		{"clear", `"range":"A1:B5","scope":"content"`, "+cells-clear"},
		{"replace", `"find":"old","replacement":""`, "+cells-replace"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			f := &fakeRunner{}
			p := New(Config{}, f)
			raw := json.RawMessage(`{"operation":"` + tc.operation + `","options":{"spreadsheet_token":"book","sheet_id":"s1","dry_run":true,` + tc.extra + `},"confirm":true}`)
			r := p.Call(approved(), "sheet_write", raw)
			if !r.OK || r.ExecutionState != "not_executed" || r.Verification != "preview" || len(f.calls) != 2 || !strings.Contains(f.calls[1], tc.command) {
				t.Fatalf("preview result=%+v calls=%v", r, f.calls)
			}
			if tc.operation == "clear" && !strings.Contains(f.calls[1], "--yes") {
				t.Fatal("clear did not include required high-risk confirmation")
			}
		})
	}
}

func TestSheetTargetValidationRejectsUnsupportedOrAmbiguousArguments(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"delete","options":{"spreadsheet_token":"book","sheet_name":"P15"},"confirm":true}`,
		`{"operation":"delete","options":{"spreadsheet_token":"book","sheet_id":"s1","sheet_name":"P15"},"confirm":true}`,
		`{"operation":"delete","options":{"spreadsheet_token":"book","sheet_id":"s1","title":"unexpected"},"confirm":true}`,
	} {
		f := &fakeRunner{}
		r := New(Config{}, f).Call(approved(), "sheet_structure", json.RawMessage(raw))
		if r.OK || len(f.calls) != 0 {
			t.Fatalf("ambiguous target was dispatched: %+v calls=%v", r, f.calls)
		}
	}
}

func TestSheetStructureCommonOperations(t *testing.T) {
	for _, tc := range []struct{ operation, extra, command string }{
		{"copy", `"title":"Copy","index":0`, "+sheet-copy"},
		{"rename", `"title":"New title"`, "+sheet-rename"},
		{"move", `"index":0`, "+sheet-move"},
		{"hide", `"dry_run":false`, "+sheet-hide"},
		{"unhide", `"dry_run":false`, "+sheet-unhide"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			f := &fakeRunner{}
			p := New(Config{}, f)
			raw := json.RawMessage(`{"operation":"` + tc.operation + `","options":{"spreadsheet_token":"book","sheet_id":"s1",` + tc.extra + `},"confirm":true}`)
			r := p.Call(approved(), "sheet_structure", raw)
			if !r.OK || len(f.calls) != 3 || !strings.Contains(f.calls[1], tc.command) || !strings.Contains(f.calls[1], "--sheet-id=s1") {
				t.Fatalf("result=%+v calls=%v", r, f.calls)
			}
		})
	}
}

func TestCopyPermissionFailureDoesNotInventCopy(t *testing.T) {
	f := &fakeRunner{fn: func(string, []string, []byte) (Output, error) {
		return Output{Code: 1, Stderr: []byte(`{"ok":false,"error":{"type":"permission","missing_scopes":["docs:document:copy"]}}`)}, nil
	}}
	p := New(Config{}, f)
	r := p.Call(approved(), "drive_write", json.RawMessage(`{"operation":"copy","options":{"token":"book","type":"sheet","name":"Copy","folder_token":"my_space"},"confirm":true}`))
	if r.OK || r.ExecutionState == "submitted" || len(f.calls) != 1 || r.Diagnostics == nil || len(r.Diagnostics.MissingScopes) != 1 || r.Diagnostics.MissingScopes[0] != "docs:document:copy" {
		t.Fatalf("failed copy was reported as existing: %+v calls=%v", r, f.calls)
	}
}
