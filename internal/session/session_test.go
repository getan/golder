package session

// Tests for local JSONL session persistence and resume (US-024, #43). They
// cover the write→read round-trip, listing order, resume into an AgentContext,
// schema-version guarding, and append — driving the real filesystem via
// t.TempDir(), the standard Go pattern for behavior tests.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// writeFile writes content to path (test helper for hand-crafted fixtures).
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// sampleMessages returns a small multi-turn transcript: user prompt, assistant
// with a tool call, tool result, then a final assistant reply.
func sampleMessages() agentcore.MessageList {
	return agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("read main.go")}},
		agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Content:    agentcore.ContentList{agentcore.NewTextContent("Reading it."), agentcore.NewToolCallContent("call-1", "read", []byte(`{"path":"main.go"}`))},
			StopReason: agentcore.StopReasonToolUse,
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "call-1",
			ToolName:   "read",
			Content:    agentcore.ContentList{agentcore.NewTextContent("package main")},
		},
		agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Content:    agentcore.ContentList{agentcore.NewTextContent("It is package main.")},
			StopReason: agentcore.StopReasonEndTurn,
		},
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// TestSaveLoadRoundTrip is the core acceptance check: a saved session loads
// back with an identical header and message sequence.
func TestSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 10, 14, 25, 30, 0, time.UTC)
	header := SessionHeader{
		ID:           NewID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        "anthropic/claude-opus-4",
		Provider:     "anthropic",
		SystemPrompt: "You are golder.",
	}
	msgs := sampleMessages()
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	gotHeader, gotMsgs, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotHeader.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", gotHeader.Version, SchemaVersion)
	}
	if gotHeader.Model != header.Model || gotHeader.SystemPrompt != header.SystemPrompt {
		t.Errorf("header mismatch: %+v", gotHeader)
	}
	if len(gotMsgs) != len(msgs) {
		t.Fatalf("message count = %d, want %d", len(gotMsgs), len(msgs))
	}
	// Roles must round-trip in order.
	wantRoles := []string{agentcore.RoleUser, agentcore.RoleAssistant, agentcore.RoleToolResult, agentcore.RoleAssistant}
	for i, m := range gotMsgs {
		if m.Role() != wantRoles[i] {
			t.Errorf("message[%d] role = %q, want %q", i, m.Role(), wantRoles[i])
		}
	}
	// The assistant tool call must survive the round-trip.
	a, ok := gotMsgs[1].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("message[1] is not AssistantMessage: %T", gotMsgs[1])
	}
	calls := a.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read" {
		t.Errorf("tool calls = %+v, want one 'read'", calls)
	}
}

// TestSaveLoadMalformedToolArguments verifies a transcript containing a tool
// call whose arguments are syntactically invalid JSON (as a model can stream)
// still saves and loads, rather than aborting the whole session write. This is
// the regression for the "session save failed: ... invalid character '{' after
// object key:value pair" crash.
func TestSaveLoadMalformedToolArguments(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	h := SessionHeader{ID: "malformed", CreatedAt: now, UpdatedAt: now}
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("go")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewToolCallContent("c1", "todo", []byte(`{"todos": []{}"content": ""x"}`)),
			},
			StopReason: agentcore.StopReasonToolUse,
		},
	}
	if err := s.Save(h, msgs); err != nil {
		t.Fatalf("Save with malformed tool args: %v", err)
	}
	_, got, err := s.Load("malformed")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("message count = %d, want 2", len(got))
	}
	a, ok := got[1].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("message[1] is not AssistantMessage: %T", got[1])
	}
	if calls := a.ToolCalls(); len(calls) != 1 || calls[0].Name != "todo" {
		t.Errorf("tool calls = %+v, want one 'todo'", calls)
	}
}

// TestSaveOverwrites verifies Save replaces an existing session file (same id)
// atomically rather than appending.
func TestSaveOverwrites(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	h := SessionHeader{ID: "fixed", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	shorter := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}}}
	if err := s.Save(h, shorter); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	_, msgs, err := s.Load("fixed")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("after overwrite, message count = %d, want 1", len(msgs))
	}
}

// TestListSortedByUpdatedDesc verifies List returns sessions most-recent-first.
func TestListSortedByUpdatedDesc(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"old", "mid", "new"} {
		h := SessionHeader{
			ID:        id,
			CreatedAt: base,
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := s.Save(h, sampleMessages()); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	headers, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(headers) != 3 {
		t.Fatalf("List count = %d, want 3", len(headers))
	}
	wantOrder := []string{"new", "mid", "old"}
	for i, h := range headers {
		if h.ID != wantOrder[i] {
			t.Errorf("List[%d].ID = %q, want %q", i, h.ID, wantOrder[i])
		}
	}
}

// TestLoadRejectsNewerSchema verifies a file whose version is newer than the
// binary supports is rejected rather than silently misread.
func TestLoadRejectsNewerSchema(t *testing.T) {
	s := newStore(t)
	// Hand-write a session file with a future version.
	future := `{"version":9999,"id":"future","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n"
	path := filepath.Join(s.Dir(), FileName("future"))
	if err := writeFile(path, future); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, _, err := s.Load("future"); err == nil {
		t.Error("Load must reject a newer schema version")
	}
}

// TestLoadV1FileStillReadable verifies the v2 schema bump is backward-compatible:
// an old v1 session file (no compaction lines) loads without error.
func TestLoadV1FileStillReadable(t *testing.T) {
	s := newStore(t)
	v1 := `{"version":1,"id":"old","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n" +
		`{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":0}` + "\n"
	path := filepath.Join(s.Dir(), FileName("old"))
	if err := writeFile(path, v1); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	h, msgs, err := s.Load("old")
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if h.Version != 1 {
		t.Fatalf("version: got %d, want 1", h.Version)
	}
	if len(msgs) != 1 || msgs[0].Role() != agentcore.RoleUser {
		t.Fatalf("messages: got %+v", msgs)
	}
}

// TestSaveWritesV3TreeEntries verifies Save persists each message as a wrapped
// v3 entry: the file header is version 3, every entry carries a non-empty id,
// the first entry is a root (empty parentId), and each subsequent entry's
// parentId chains to the previous entry's id — a linear tree.
func TestSaveWritesV3TreeEntries(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h, entries, err := s.LoadEntries(header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if h.Version != 3 {
		t.Fatalf("version = %d, want 3", h.Version)
	}
	if len(entries) != 4 {
		t.Fatalf("entry count = %d, want 4", len(entries))
	}
	if entries[0].ParentID != "" {
		t.Errorf("root entry parentId = %q, want empty", entries[0].ParentID)
	}
	seen := map[string]bool{}
	for i, e := range entries {
		if e.ID == "" {
			t.Errorf("entry[%d] has empty id", i)
		}
		if seen[e.ID] {
			t.Errorf("entry[%d] id %q is duplicated", i, e.ID)
		}
		seen[e.ID] = true
		if i > 0 && e.ParentID != entries[i-1].ID {
			t.Errorf("entry[%d] parentId = %q, want %q (previous entry)", i, e.ParentID, entries[i-1].ID)
		}
	}
}

// TestPathToLeaf verifies PathToLeaf walks the parentId chain from a leaf back
// to the root and returns the entries in root→leaf order.
func TestPathToLeaf(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(header.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	leaf := entries[len(entries)-1]
	path := PathToLeaf(entries, leaf.ID)
	if len(path) != len(entries) {
		t.Fatalf("path length = %d, want %d (linear session)", len(path), len(entries))
	}
	for i := range entries {
		if path[i].ID != entries[i].ID {
			t.Errorf("path[%d].ID = %q, want %q", i, path[i].ID, entries[i].ID)
		}
	}
	// A mid-chain leaf yields only its ancestors + itself.
	mid := PathToLeaf(entries, entries[1].ID)
	if len(mid) != 2 || mid[0].ID != entries[0].ID || mid[1].ID != entries[1].ID {
		t.Errorf("PathToLeaf(entries[1]) = %+v, want [root, entries[1]]", mid)
	}
	// Unknown / empty leaf ids yield nil.
	if got := PathToLeaf(entries, "nope"); got != nil {
		t.Errorf("PathToLeaf(unknown) = %+v, want nil", got)
	}
	if got := PathToLeaf(entries, ""); got != nil {
		t.Errorf("PathToLeaf(empty) = %+v, want nil", got)
	}
}

// TestLoadV2FileMigratesToEntries verifies a v2 file (bare message lines, no
// id/parentId) still loads and resumes: readSession back-fills a synthesized id
// per line and chains parentId to the previous entry, so the migrated entries
// form a linear tree while the flat Load view is unchanged.
func TestLoadV2FileMigratesToEntries(t *testing.T) {
	s := newStore(t)
	v2 := `{"version":2,"id":"legacy","createdAt":"2026-07-10T00:00:00Z","updatedAt":"2026-07-10T00:00:00Z"}` + "\n" +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","content":[{"type":"text","text":"hello"}],"stopReason":"end_turn"}` + "\n"
	path := filepath.Join(s.Dir(), FileName("legacy"))
	if err := writeFile(path, v2); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Flat Load view is unchanged by the migration.
	h, msgs, err := s.Load("legacy")
	if err != nil {
		t.Fatalf("Load v2: %v", err)
	}
	if h.Version != 2 {
		t.Fatalf("version = %d, want 2", h.Version)
	}
	if len(msgs) != 2 || msgs[0].Role() != agentcore.RoleUser || msgs[1].Role() != agentcore.RoleAssistant {
		t.Fatalf("messages: got %+v", msgs)
	}
	// Entry view is back-filled into a linear tree.
	_, entries, err := s.LoadEntries("legacy")
	if err != nil {
		t.Fatalf("LoadEntries v2: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].ID == "" || entries[0].ParentID != "" {
		t.Errorf("root entry = %+v, want non-empty id and empty parentId", entries[0])
	}
	if entries[1].ParentID != entries[0].ID {
		t.Errorf("entry[1].parentId = %q, want %q", entries[1].ParentID, entries[0].ID)
	}
	// The migrated entries reconstruct the full conversation via PathToLeaf.
	path2 := PathToLeaf(entries, entries[1].ID)
	if len(path2) != 2 {
		t.Errorf("PathToLeaf on migrated v2 = %d entries, want 2", len(path2))
	}
}

// TestAppendPreservesChain verifies Append grows the linear tree: after
// appending, the file still loads with a valid root and an unbroken parentId
// chain across the combined message set.
func TestAppendPreservesChain(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	h := SessionHeader{ID: "grow", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	extra := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("more")}}}
	if err := s.Append("grow", now.Add(time.Minute), extra); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_, entries, err := s.LoadEntries("grow")
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("entry count = %d, want 5", len(entries))
	}
	if entries[0].ParentID != "" {
		t.Errorf("root parentId = %q, want empty", entries[0].ParentID)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentID != entries[i-1].ID {
			t.Errorf("entry[%d] parentId = %q, want %q", i, entries[i].ParentID, entries[i-1].ID)
		}
	}
	if path := PathToLeaf(entries, entries[4].ID); len(path) != 5 {
		t.Errorf("PathToLeaf after append = %d, want 5", len(path))
	}
}

// TestForkClonesFullConversation verifies Fork(sourceID, lastLeaf) — the /clone
// case — copies the entire conversation verbatim into a new, independent session:
// the new header records ParentSession, the copied entries keep their ids, and
// appending to the fork does NOT touch the source (branch isolation).
func TestForkClonesFullConversation(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	src := SessionHeader{
		ID: NewID(now), CreatedAt: now, UpdatedAt: now, Model: "m", Provider: "p", SystemPrompt: "sp",
		// The user-authored prompt inputs ride along with the rest of the header
		// metadata: a fork resumes the same way its source does.
		BaseInstruction: "custom base", AppendInstructions: []string{"appendix"},
	}
	if err := s.Save(src, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, srcEntries, err := s.LoadEntries(src.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	leaf := srcEntries[len(srcEntries)-1].ID

	forkHeader, forkEntries, err := s.Fork(src.ID, leaf, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forkHeader.ID == src.ID {
		t.Fatal("fork must get a new id, got the source id")
	}
	if forkHeader.ParentSession != src.ID {
		t.Errorf("ParentSession = %q, want %q", forkHeader.ParentSession, src.ID)
	}
	// Header metadata is inherited from the source.
	if forkHeader.Model != "m" || forkHeader.Provider != "p" || forkHeader.SystemPrompt != "sp" {
		t.Errorf("fork header did not inherit source metadata: %+v", forkHeader)
	}
	if forkHeader.BaseInstruction != "custom base" || len(forkHeader.AppendInstructions) != 1 {
		t.Errorf("fork header did not inherit the user prompt inputs: %+v", forkHeader)
	}
	if len(forkEntries) != len(srcEntries) {
		t.Fatalf("fork entry count = %d, want %d (full clone)", len(forkEntries), len(srcEntries))
	}
	// Copied entries keep their ids/parentIds verbatim.
	for i := range srcEntries {
		if forkEntries[i].ID != srcEntries[i].ID || forkEntries[i].ParentID != srcEntries[i].ParentID {
			t.Errorf("entry[%d] id/parent = (%q,%q), want (%q,%q)", i,
				forkEntries[i].ID, forkEntries[i].ParentID, srcEntries[i].ID, srcEntries[i].ParentID)
		}
	}

	// Branch isolation: appending to the fork must not change the source.
	extra := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("on the fork only")}}}
	if err := s.Append(forkHeader.ID, now.Add(2*time.Hour), extra); err != nil {
		t.Fatalf("Append to fork: %v", err)
	}
	_, srcAfter, err := s.Load(src.ID)
	if err != nil {
		t.Fatalf("Load source: %v", err)
	}
	if len(srcAfter) != len(sampleMessages()) {
		t.Errorf("source message count changed to %d after fork append, want %d (branches must be isolated)", len(srcAfter), len(sampleMessages()))
	}
	_, forkAfter, err := s.Load(forkHeader.ID)
	if err != nil {
		t.Fatalf("Load fork: %v", err)
	}
	if len(forkAfter) != len(sampleMessages())+1 {
		t.Errorf("fork message count = %d, want %d", len(forkAfter), len(sampleMessages())+1)
	}
}

// TestForkBeforeUserMessage verifies Fork(sourceID, parentOfUserMsg) — the
// /fork case — copies only the prefix up to (excluding) a chosen user message,
// so the branch can re-prompt from that point. Forking before the very first
// user message (empty leafID) yields an empty session rooted at the source.
func TestForkBeforeUserMessage(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	src := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(src, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(src.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	// sampleMessages()[0] is the first user message (a root). Forking before it
	// uses its ParentID (empty) → an empty branch.
	if entries[0].ParentID != "" {
		t.Fatalf("precondition: first entry should be a root")
	}
	emptyHeader, emptyPath, err := s.Fork(src.ID, entries[0].ParentID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Fork before first message: %v", err)
	}
	if len(emptyPath) != 0 {
		t.Errorf("fork before first message = %d entries, want 0", len(emptyPath))
	}
	if emptyHeader.ParentSession != src.ID {
		t.Errorf("ParentSession = %q, want %q", emptyHeader.ParentSession, src.ID)
	}
	// Reloading the empty fork yields a valid, empty session.
	_, reload, err := s.LoadEntries(emptyHeader.ID)
	if err != nil {
		t.Fatalf("LoadEntries(empty fork): %v", err)
	}
	if len(reload) != 0 {
		t.Errorf("reloaded empty fork = %d entries, want 0", len(reload))
	}

	// Forking before the SECOND-turn user message (there is only one user message
	// in sampleMessages, so simulate a two-user transcript) — copy just the prefix.
	// Here we fork at the parent of the last entry to get all but the last message.
	lastParent := entries[len(entries)-1].ParentID
	_, prefix, err := s.Fork(src.ID, lastParent, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Fork at last parent: %v", err)
	}
	if len(prefix) != len(entries)-1 {
		t.Errorf("prefix fork = %d entries, want %d", len(prefix), len(entries)-1)
	}
}

// TestAppendBranchGrowsTree verifies AppendBranch (US-007, #123) preserves all
// existing entries and chains new messages from a chosen parent leaf — so
// switching the active leaf to a historical entry and continuing produces a real
// sibling branch on disk rather than truncating history.
func TestAppendBranchGrowsTree(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	h := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(h.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	// Branch from the FIRST entry (the root user message): append a new user turn
	// as its child. The result must keep every original entry plus the new one.
	branchParent := entries[0].ID
	extra := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("a different question")}},
	}
	leaf, err := s.AppendBranch(h, branchParent, extra)
	if err != nil {
		t.Fatalf("AppendBranch: %v", err)
	}
	_, after, err := s.LoadEntries(h.ID)
	if err != nil {
		t.Fatalf("LoadEntries after branch: %v", err)
	}
	if len(after) != len(entries)+1 {
		t.Fatalf("entry count = %d, want %d (nothing dropped, one added)", len(after), len(entries)+1)
	}
	// The new leaf descends from the chosen parent.
	var newLeaf *Entry
	for i := range after {
		if after[i].ID == leaf {
			newLeaf = &after[i]
		}
	}
	if newLeaf == nil {
		t.Fatalf("new leaf %q not found in reloaded entries", leaf)
	}
	if newLeaf.ParentID != branchParent {
		t.Errorf("new leaf parent = %q, want %q", newLeaf.ParentID, branchParent)
	}
	// The root now has two children: the original second entry and the new leaf —
	// a genuine branch point.
	kids := 0
	for _, e := range after {
		if e.ParentID == branchParent {
			kids++
		}
	}
	if kids != 2 {
		t.Errorf("branch point should have 2 children, got %d", kids)
	}
	// PathToLeaf to the new leaf yields exactly [root, newLeaf].
	path := PathToLeaf(after, leaf)
	if len(path) != 2 || path[0].ID != branchParent || path[1].ID != leaf {
		t.Errorf("PathToLeaf(newLeaf) = %v, want [root, newLeaf]", pathIDs(path))
	}
}

// TestAppendBranchCreatesFileWhenMissing verifies AppendBranch creates the
// session file on first use (the fresh-session first-turn case) rather than
// erroring like Append does.
func TestAppendBranchCreatesFileWhenMissing(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	h := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
	}
	leaf, err := s.AppendBranch(h, "", msgs)
	if err != nil {
		t.Fatalf("AppendBranch on missing file: %v", err)
	}
	_, entries, err := s.LoadEntries(h.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != leaf || entries[0].ParentID != "" {
		t.Errorf("expected one root entry with id=%q, got %v", leaf, entries)
	}
}

// TestRenderTreeLinesMarksCurrentAndBranches verifies the pure-text tree render
// (US-007, #123): every entry gets one numbered-able line in render order, the
// entry behind the active leaf is tagged Current, and a branch point produces
// two child rows.
func TestRenderTreeLinesMarksCurrentAndBranches(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	h := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	if err := s.Save(h, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, entries, err := s.LoadEntries(h.ID)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	// Branch off the root to create a fork point.
	if _, err := s.AppendBranch(h, entries[0].ID, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("sibling turn")}},
	}); err != nil {
		t.Fatalf("AppendBranch: %v", err)
	}
	_, after, err := s.LoadEntries(h.ID)
	if err != nil {
		t.Fatalf("LoadEntries after branch: %v", err)
	}

	leaf := after[len(after)-1].ID
	lines := RenderTreeLines(after, leaf)
	if len(lines) != len(after) {
		t.Fatalf("render produced %d lines, want %d (one per entry)", len(lines), len(after))
	}
	// Exactly one line is tagged as current, and it is the leaf.
	current := 0
	for _, l := range lines {
		if l.Current {
			current++
			if l.Entry.ID != leaf {
				t.Errorf("current marker on %q, want leaf %q", l.Entry.ID, leaf)
			}
		}
	}
	if current != 1 {
		t.Errorf("expected exactly one current line, got %d", current)
	}
	// Connector characters must appear (readable branch structure).
	joined := ""
	for _, l := range lines {
		joined += l.Text + "\n"
	}
	if !strings.Contains(joined, "├─") && !strings.Contains(joined, "└─") {
		t.Errorf("tree render lacks connectors:\n%s", joined)
	}
	// The first line is a root (no connector prefix) rendering the root user msg.
	if !strings.HasPrefix(lines[0].Text, "user:") {
		t.Errorf("first render line should be the root user message, got %q", lines[0].Text)
	}
}

// treeUserEntry builds a user-message entry for hand-crafted forest tests.
func treeUserEntry(id, parent, text string, at time.Time) Entry {
	return Entry{
		ID:        id,
		ParentID:  parent,
		Timestamp: at,
		Message: agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
		},
	}
}

// TestRenderTreeLinesChainCompressionKeepsWidthBounded is the regression test
// for the /tree deep-leaf overflow: a linear conversation renders flat (no
// indent growth with message count), so a deep leaf's text stays on screen.
func TestRenderTreeLinesChainCompressionKeepsWidthBounded(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	const n = 60
	var entries []Entry
	parent := ""
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("e%02d", i)
		entries = append(entries, treeUserEntry(id, parent, fmt.Sprintf("message %d", i), now.Add(time.Duration(i)*time.Second)))
		parent = id
	}
	lines := RenderTreeLines(entries, parent)
	if len(lines) != n {
		t.Fatalf("render produced %d lines, want %d", len(lines), n)
	}
	for i, l := range lines {
		if strings.HasPrefix(l.Text, " ") || strings.HasPrefix(l.Text, "│") {
			t.Fatalf("line %d is indented (%q): linear chains must render flat", i, l.Text)
		}
		if want := fmt.Sprintf("message %d", i); !strings.Contains(l.Text, want) {
			t.Fatalf("line %d lost its text %q: %q", i, want, l.Text)
		}
	}
	if !lines[n-1].Current {
		t.Error("the last line should be the current leaf")
	}
}

// TestRenderTreeLinesForkIndentIsBounded verifies connectors appear only at
// real forks — chains below them keep a constant column — and that even
// pathologically nested forks stay within the cap: a summary never starts past
// treeMaxIndentCells cells (24 cols) plus its own connector cell (3 cols).
func TestRenderTreeLinesForkIndentIsBounded(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	var entries []Entry
	add := func(id, parent, text string) {
		entries = append(entries, treeUserEntry(id, parent, text, now.Add(time.Duration(len(entries))*time.Second)))
	}
	add("f00", "", "fork 0")
	for i := 1; i <= 20; i++ {
		parent := fmt.Sprintf("f%02d", i-1)
		add(fmt.Sprintf("a%02d", i), parent, fmt.Sprintf("leaf %d", i))
		add(fmt.Sprintf("f%02d", i), parent, fmt.Sprintf("fork %d", i))
	}

	lines := RenderTreeLines(entries, "f20")
	if len(lines) != len(entries) {
		t.Fatalf("render produced %d lines, want %d", len(lines), len(entries))
	}
	joined := ""
	for _, l := range lines {
		joined += l.Text + "\n"
	}
	if !strings.Contains(joined, "├─ ") || !strings.Contains(joined, "└─ ") {
		t.Errorf("fork rows should carry ├─/└─ connectors:\n%s", joined)
	}
	if !strings.Contains(joined, "⋯") {
		t.Errorf("deep nested forks should elide outer indent cells with ⋯:\n%s", joined)
	}
	for _, l := range lines {
		col := strings.Index(l.Text, "user: ")
		if col < 0 {
			t.Fatalf("line lost its summary: %q", l.Text)
		}
		if cols := len([]rune(l.Text[:col])); cols > treeMaxIndentCells*3+3 {
			t.Errorf("summary starts at column %d (over cap): %q", cols, l.Text)
		}
	}
}

// pathIDs is a small test helper: the ids of a path, for readable failures.
func pathIDs(path []Entry) []string {
	ids := make([]string, len(path))
	for i, e := range path {
		ids[i] = e.ID
	}
	return ids
}

// through the JSONL store as a first-class message line under schema v2.
func TestSaveLoadCompactionEntry(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	msgs := agentcore.MessageList{
		agentcore.CompactionMessage{
			RoleField:    agentcore.RoleCompaction,
			Summary:      "## Goal\nship #119",
			TokensBefore: 12345,
			Details:      []byte(`{"readFiles":["a.go"],"modifiedFiles":["b.go"]}`),
			Timestamp:    now.UnixMilli(),
		},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("continue")}},
	}
	if err := s.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, back, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != SchemaVersion {
		t.Fatalf("version: got %d, want %d", loaded.Version, SchemaVersion)
	}
	if len(back) != 2 || back[0].Role() != agentcore.RoleCompaction {
		t.Fatalf("messages: got %+v", back)
	}
	cm, ok := back[0].(agentcore.CompactionMessage)
	if !ok {
		t.Fatalf("first message is not a CompactionMessage: %T", back[0])
	}
	if cm.Summary != "## Goal\nship #119" || cm.TokensBefore != 12345 {
		t.Fatalf("compaction fields: %+v", cm)
	}
}

// TestContextHeaderFieldsRoundTrip verifies the additive "infinite context"
// header fields (#480) — ContextFrom/ContextWatermark — survive a Save→Load
// round-trip through the real session file path.
func TestContextHeaderFieldsRoundTrip(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	header := SessionHeader{
		ID:               NewID(now),
		CreatedAt:        now,
		UpdatedAt:        now,
		ContextFrom:      "parent-sess-123",
		ContextWatermark: 37,
	}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ContextFrom != "parent-sess-123" {
		t.Errorf("ContextFrom = %q, want %q", got.ContextFrom, "parent-sess-123")
	}
	if got.ContextWatermark != 37 {
		t.Errorf("ContextWatermark = %d, want 37", got.ContextWatermark)
	}
}

// TestContextHeaderFieldsOmittedWhenZero verifies the fields are omitempty: a
// header without them serializes without the keys, keeping v1/v2/v3 files
// byte-compatible for the common no-inherited-context case.
func TestContextHeaderFieldsOmittedWhenZero(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	header := SessionHeader{ID: "no-ctx", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir(), FileName("no-ctx")))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	headerLine := strings.SplitN(string(raw), "\n", 2)[0]
	if strings.Contains(headerLine, "contextFrom") || strings.Contains(headerLine, "contextWatermark") {
		t.Errorf("zero context fields must be omitted; header line = %s", headerLine)
	}
}

// TestSetContextInheritanceRoundTrips verifies that a session created with
// inheritance set via SetContextInheritance writes both fields and exposes them
// (via the ContextInheritance accessor) when read back.
func TestSetContextInheritanceRoundTrips(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	header := SessionHeader{ID: NewID(now), CreatedAt: now, UpdatedAt: now}
	SetContextInheritance(&header, "src-sess-42", 12)

	if err := s.Save(header, sampleMessages()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := s.Load(header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fromID, watermark, ok := got.ContextInheritance()
	if !ok {
		t.Fatalf("ContextInheritance ok = false, want true")
	}
	if fromID != "src-sess-42" || watermark != 12 {
		t.Errorf("ContextInheritance = (%q, %d), want (%q, 12)", fromID, watermark, "src-sess-42")
	}
	if !got.HasContextInheritance() {
		t.Errorf("HasContextInheritance = false, want true")
	}
}

// TestSetContextInheritanceClears verifies an empty source or non-positive
// watermark clears both fields, so the header round-trips as a no-inheritance
// session (omitempty keeps it byte-compatible with older files).
func TestSetContextInheritanceClears(t *testing.T) {
	cases := []struct {
		name      string
		fromID    string
		watermark int
	}{
		{"empty source", "", 5},
		{"zero watermark", "src", 0},
		{"negative watermark", "src", -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := SessionHeader{ID: "h", ContextFrom: "stale", ContextWatermark: 99}
			SetContextInheritance(&header, tc.fromID, tc.watermark)
			if header.ContextFrom != "" || header.ContextWatermark != 0 {
				t.Errorf("expected cleared inheritance, got (%q, %d)", header.ContextFrom, header.ContextWatermark)
			}
			if header.HasContextInheritance() {
				t.Errorf("HasContextInheritance = true after clear")
			}
		})
	}
}

// TestContextInheritanceRejectsPartial verifies the accessor treats a header
// with only one of the two fields as "no inheritance" (ok=false) rather than
// reporting a half-configured record.
func TestContextInheritanceRejectsPartial(t *testing.T) {
	onlyFrom := SessionHeader{ContextFrom: "src"}
	if _, _, ok := onlyFrom.ContextInheritance(); ok {
		t.Errorf("ok = true for header with source but no watermark")
	}
	onlyWatermark := SessionHeader{ContextWatermark: 8}
	if _, _, ok := onlyWatermark.ContextInheritance(); ok {
		t.Errorf("ok = true for header with watermark but no source")
	}
}

// TestLoadedSessionsWithoutInheritanceReadZero verifies backward compatibility:
// v1/v2/v3 sessions written without the inheritance fields load with zero-value
// ContextFrom/ContextWatermark and report HasContextInheritance()=false.
func TestLoadedSessionsWithoutInheritanceReadZero(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// v3 written through the normal path (no inheritance set).
	v3 := SessionHeader{ID: "v3-noctx", CreatedAt: now, UpdatedAt: now}
	if err := s.Save(v3, sampleMessages()); err != nil {
		t.Fatalf("Save v3: %v", err)
	}

	// Hand-crafted v1 and v2 fixtures (bare message lines, no context fields).
	msgLine := `{"role":"user","content":[{"type":"text","text":"hi"}]}`
	if err := writeFile(filepath.Join(s.Dir(), FileName("v1-noctx")),
		`{"version":1,"id":"v1-noctx","createdAt":"2026-01-02T03:04:05Z","updatedAt":"2026-01-02T03:04:05Z"}`+"\n"+msgLine+"\n"); err != nil {
		t.Fatalf("write v1 fixture: %v", err)
	}
	if err := writeFile(filepath.Join(s.Dir(), FileName("v2-noctx")),
		`{"version":2,"id":"v2-noctx","createdAt":"2026-01-02T03:04:05Z","updatedAt":"2026-01-02T03:04:05Z"}`+"\n"+msgLine+"\n"); err != nil {
		t.Fatalf("write v2 fixture: %v", err)
	}

	for _, id := range []string{"v3-noctx", "v1-noctx", "v2-noctx"} {
		got, _, err := s.Load(id)
		if err != nil {
			t.Fatalf("Load %s: %v", id, err)
		}
		if got.ContextFrom != "" || got.ContextWatermark != 0 {
			t.Errorf("%s: expected zero inheritance, got (%q, %d)", id, got.ContextFrom, got.ContextWatermark)
		}
		if got.HasContextInheritance() {
			t.Errorf("%s: HasContextInheritance = true, want false", id)
		}
	}
}
