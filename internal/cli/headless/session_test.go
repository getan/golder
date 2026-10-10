package headless

// Tests for headless session persistence and resume (session id in stream-json
// + --resume for headless runs). openHeadlessSession/persist are exercised
// directly against an isolated GOLDER_HOME so a headless run's session round-trips
// without spawning a provider.

import (
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

func textUser(s string) agentcore.UserMessage {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
}

func textAssistant(s string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
}

// TestOpenHeadlessSessionFresh verifies a fresh headless session gets a new id
// and empty prior messages, and that persist writes the run's messages so they
// can be resumed.
func TestOpenHeadlessSessionFresh(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())

	prior, hs, err := openHeadlessSession("", "faux-model", "faux", "sys prompt", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("openHeadlessSession fresh: %v", err)
	}
	if len(prior) != 0 {
		t.Errorf("fresh session must have no prior messages, got %d", len(prior))
	}
	if hs.header.ID == "" {
		t.Fatal("fresh session must have a non-empty id")
	}
	if hs.header.SystemPrompt != "sys prompt" {
		t.Errorf("header SystemPrompt = %q, want the passed prompt", hs.header.SystemPrompt)
	}

	// Simulate a completed run: prompt + assistant reply.
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("1+1=?"), textAssistant("2")}}
	if err := hs.persist(agentCtx); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// The session must now be loadable with both messages.
	_, msgs, err := hs.store.Load(hs.header.ID)
	if err != nil {
		t.Fatalf("Load persisted session: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted session has %d messages, want 2", len(msgs))
	}
}

// TestOpenHeadlessSessionResume verifies that resuming seeds the prior messages
// and that a subsequent run appends only the new tail as a branch, so the
// session grows rather than being rewritten.
func TestOpenHeadlessSessionResume(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())

	// First run: create and persist a session.
	_, hs1, err := openHeadlessSession("", "faux-model", "faux", "sys", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("first openHeadlessSession: %v", err)
	}
	ctx1 := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("first"), textAssistant("reply1")}}
	if err := hs1.persist(ctx1); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	sessID := hs1.header.ID

	// Second run: resume the session id.
	prior, hs2, err := openHeadlessSession(sessID, "faux-model", "faux", "sys", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("resume openHeadlessSession: %v", err)
	}
	if len(prior) != 2 {
		t.Fatalf("resume must seed %d prior messages, got %d", 2, len(prior))
	}
	if hs2.header.ID != sessID {
		t.Errorf("resumed session id = %q, want %q", hs2.header.ID, sessID)
	}
	if hs2.persisted != 2 {
		t.Errorf("resumed persisted cursor = %d, want 2", hs2.persisted)
	}

	// A second turn appends its new tail.
	ctx2 := &agentcore.AgentContext{Messages: append(prior, textUser("second"), textAssistant("reply2"))}
	if err := hs2.persist(ctx2); err != nil {
		t.Fatalf("second persist: %v", err)
	}
	_, msgs, err := hs2.store.Load(sessID)
	if err != nil {
		t.Fatalf("Load after second turn: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("session after two turns has %d messages, want 4", len(msgs))
	}
}

// TestHeadlessPersistNoop verifies persist is a no-op (no error, no growth) when
// the run produced nothing new past what was already persisted.
func TestHeadlessPersistNoop(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	_, hs, err := openHeadlessSession("", "m", "p", "s", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("x"), textAssistant("y")}}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	// Persisting again with no new messages must not error and must not duplicate.
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("noop persist: %v", err)
	}
	_, msgs, err := hs.store.Load(hs.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("noop persist changed message count to %d, want 2", len(msgs))
	}
}

// TestHeadlessPersistCompactionShrink verifies persist tolerates the context
// being rebuilt to fewer messages than were on disk before the run (mid-run
// compaction replaces agentCtx.Messages). The persisted cursor is clamped so
// the tail slice stays in bounds rather than panicking.
func TestHeadlessPersistCompactionShrink(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	_, hs, err := openHeadlessSession("", "m", "p", "s", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	// Persist four messages, advancing the cursor to 4.
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{textUser("a"), textAssistant("b"), textUser("c"), textAssistant("d")}}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	if hs.persisted != 4 {
		t.Fatalf("cursor = %d, want 4", hs.persisted)
	}
	// Simulate compaction: the context is rebuilt to fewer messages than the
	// cursor. persist must not panic on the out-of-range slice.
	ctx.Messages = agentcore.MessageList{textAssistant("summary"), textUser("e")}
	if err := hs.persist(ctx); err != nil {
		t.Fatalf("persist after compaction shrink: %v", err)
	}
	if hs.persisted != 2 {
		t.Errorf("cursor after clamp = %d, want 2", hs.persisted)
	}
}

// TestContinueTargetPrefersCurrentProject: --continue resolves the newest
// session of its own project even when another project has a newer one, and
// only falls back across projects with a note naming the directory it took the
// session from.
func TestContinueTargetPrefersCurrentProject(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := SessionStore()
	if err != nil {
		t.Fatalf("SessionStore: %v", err)
	}
	proj := t.TempDir()
	other := t.TempDir()
	base := time.Now().UTC()
	save := func(cwd string, at time.Time) string {
		h := session.SessionHeader{ID: session.NewID(at), CreatedAt: at, UpdatedAt: at, Cwd: cwd}
		if err := store.Save(h, nil); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return h.ID
	}

	// Another project is the globally newest; this project's own session wins.
	foreign := save(other, base)
	local := save(proj, base.Add(-time.Minute))
	id, note, err := ContinueTarget(proj)
	if err != nil {
		t.Fatalf("ContinueTarget: %v", err)
	}
	if id != local {
		t.Errorf("ContinueTarget = %q, want this project's %q", id, local)
	}
	if note != "" {
		t.Errorf("note = %q, want empty when the project has its own session", note)
	}

	// A project with nothing of its own falls back to the newest session and
	// names the directory it came from.
	id, note, err = ContinueTarget(t.TempDir())
	if err != nil {
		t.Fatalf("ContinueTarget fallback: %v", err)
	}
	if id != foreign {
		t.Errorf("fallback id = %q, want the newest session %q", id, foreign)
	}
	if !strings.Contains(note, cli.SessionDirDisplay(session.SessionHeader{Cwd: other})) {
		t.Errorf("fallback note = %q, want it to name the source directory", note)
	}

	// An empty store resolves to "nothing to continue".
	empty, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if h, ok, err := cli.MostRecentSession(empty, cli.AllSessionsScope()); err != nil || ok {
		t.Errorf("empty store = (%v, %v, %v), want (zero, false, nil)", h, ok, err)
	}
}

// TestOpenHeadlessSessionRebuildsPrompt pins the resume path end to end: a
// session whose stored prompt carries an obsolete guide runs under the CURRENT
// binary's prompt, so a headless --resume is not advised by stale text.
func TestOpenHeadlessSessionRebuildsPrompt(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := SessionStore()
	if err != nil {
		t.Fatalf("SessionStore: %v", err)
	}
	now := time.Now().UTC()
	stale := "You are golder, a helpful coding agent.\n\n[obsolete guide]\n\nEnvironment:\n- Date: 2026-09-28"
	h := session.SessionHeader{ID: session.NewID(now), CreatedAt: now, UpdatedAt: now, SystemPrompt: stale}
	if err := store.Save(h, agentcore.MessageList{textUser("hi")}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, hs, err := openHeadlessSession(h.ID, "m", "p", "fresh prompt", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	if hs.header.SystemPrompt == stale {
		t.Fatal("a resumed run must not run under the stored prompt verbatim")
	}
	if !strings.HasPrefix(hs.header.SystemPrompt, runtime.DefaultBaseInstruction) {
		t.Errorf("resumed prompt should open with the current guide, got:\n%.200s", hs.header.SystemPrompt)
	}
	if strings.Contains(hs.header.SystemPrompt, "obsolete guide") {
		t.Errorf("resumed prompt must not carry the stored guide:\n%s", hs.header.SystemPrompt)
	}
}

// TestOpenHeadlessSessionKeepsRecordedInputs: a session created with
// --system-prompt / --append-system-prompt keeps those words across a resume,
// while the rest of the prompt is still rebuilt.
func TestOpenHeadlessSessionKeepsRecordedInputs(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := SessionStore()
	if err != nil {
		t.Fatalf("SessionStore: %v", err)
	}
	now := time.Now().UTC()
	h := session.SessionHeader{
		ID: session.NewID(now), CreatedAt: now, UpdatedAt: now,
		SystemPrompt:    "custom base\n\nEnvironment:\n- Date: 2026-09-28",
		BaseInstruction: "custom base", AppendInstructions: []string{"keep me"},
	}
	if err := store.Save(h, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, hs, err := openHeadlessSession(h.ID, "m", "p", "fresh prompt", runtime.PromptInputs{})
	if err != nil {
		t.Fatalf("openHeadlessSession: %v", err)
	}
	if !strings.HasPrefix(hs.header.SystemPrompt, "custom base") {
		t.Errorf("the recorded base must survive a resume, got:\n%.200s", hs.header.SystemPrompt)
	}
	if !strings.Contains(hs.header.SystemPrompt, "keep me") {
		t.Errorf("the recorded appendix must survive a resume:\n%s", hs.header.SystemPrompt)
	}
}
