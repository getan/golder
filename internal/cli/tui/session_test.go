package tui

import (
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/session"
)

// newTestStore opens a session store rooted at a temp dir so persistence/resume
// can be exercised without touching ~/.pigo.
func newTestStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// saveSession writes a linear session with the given messages and returns its id.
func saveSession(t *testing.T, store *session.Store, msgs agentcore.MessageList) string {
	t.Helper()
	now := time.Now().UTC()
	header := session.SessionHeader{
		ID:        session.NewID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     "test-model",
		Provider:  "test-provider",
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return header.ID
}

// TestResumeSeedsTranscript constructs a session with a few messages, resumes it
// through newRunSessionWithStore, seeds a transcript with the returned history,
// and asserts the initial transcript blocks carry those messages (FR-16 resume).
func TestResumeSeedsTranscript(t *testing.T) {
	store := newTestStore(t)
	id := saveSession(t, store, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello, world")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("hello back")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("second question")}},
	})

	s, history, err := newRunSessionWithStore(store, Options{ResumeID: id})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	// The persisted cursor must cover the full resumed history so the first new
	// turn appends only fresh messages, not a re-save of history.
	if s.persisted != 3 {
		t.Errorf("persisted = %d, want 3", s.persisted)
	}
	if s.curLeaf == "" {
		t.Error("curLeaf should be the resumed leaf, got empty")
	}

	tr := newTranscript(DefaultTheme())
	seedTranscript(&tr, history)

	wantTexts := []string{"hello, world", "hello back", "second question"}
	if len(tr.blocks) != len(wantTexts) {
		t.Fatalf("transcript blocks = %d, want %d", len(tr.blocks), len(wantTexts))
	}
	for i, want := range wantTexts {
		if tr.blocks[i].text != want {
			t.Errorf("block[%d] = %q, want %q", i, tr.blocks[i].text, want)
		}
	}
}

// TestBuildConfigAssembly asserts the run-config assembly maps the live config
// onto RunConfig without a live provider: the model/provider/thinking/window
// fields flow through, compaction is enabled, and the tool registry is wired.
func TestBuildConfigAssembly(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:         "opus-test",
		ProviderName:  "anthropic",
		ThinkingLevel: agentcore.ThinkingLevel("high"),
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	cfg := s.buildConfig()
	if cfg.Model != "opus-test" {
		t.Errorf("cfg.Model = %q, want opus-test", cfg.Model)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("cfg.Provider = %q, want anthropic", cfg.Provider)
	}
	if cfg.ThinkingLevel != agentcore.ThinkingLevel("high") {
		t.Errorf("cfg.ThinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
	if cfg.ContextWindow <= 0 {
		t.Errorf("cfg.ContextWindow = %d, want a positive default", cfg.ContextWindow)
	}
	if !cfg.Compaction.Enabled {
		t.Error("cfg.Compaction.Enabled = false, want true (DefaultCompactionSettings)")
	}
	if cfg.Batch.Registry == nil {
		t.Error("cfg.Batch.Registry is nil, want the assembled tool registry")
	}
	if cfg.Stream == nil {
		t.Error("cfg.Stream is nil, want a stream fn derived from the provider")
	}
}

// TestFreshSessionPersists starts a fresh session, appends a turn to the context,
// persists it, and confirms it round-trips back through the store (FR-16 persist).
func TestFreshSessionPersists(t *testing.T) {
	store := newTestStore(t)
	s, history, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if history != nil {
		t.Fatalf("fresh session history = %v, want nil", history)
	}

	s.agentCtx.Messages = append(s.agentCtx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("yo")}},
	)
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if s.persisted != 2 {
		t.Errorf("persisted = %d, want 2", s.persisted)
	}

	_, msgs, err := store.Load(s.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(msgs))
	}

	// A second persist with no new messages is a no-op.
	before := s.curLeaf
	if err := s.persist(); err != nil {
		t.Fatalf("persist (no-op): %v", err)
	}
	if s.curLeaf != before {
		t.Errorf("curLeaf changed on no-op persist: %q -> %q", before, s.curLeaf)
	}
}

// TestPersistAfterCompaction reproduces the crash where an automatic compaction
// shrinks agentCtx.Messages below the persisted cursor: an incremental
// Messages[persisted:] would panic with a slice-bounds error. persist() must
// instead re-save the flattened context and reset the cursor to the new length.
func TestPersistAfterCompaction(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	// Persist a few turns so the cursor advances past what compaction will keep.
	for i := 0; i < 4; i++ {
		s.agentCtx.Messages = append(s.agentCtx.Messages,
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q")}},
			agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a")}},
		)
	}
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if s.persisted != 8 {
		t.Fatalf("persisted = %d, want 8 before compaction", s.persisted)
	}

	// Simulate the run loop compacting: Messages is rewritten to a shorter
	// summary + tail (here just a 2-message tail), and the loop signalled it via
	// compactionMsg (which sets s.compacted).
	s.agentCtx.Messages = agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("recent q")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("recent a")}},
	}
	s.compacted = true

	if err := s.persist(); err != nil {
		t.Fatalf("persist after compaction: %v", err)
	}
	if s.compacted {
		t.Error("compacted flag should be cleared after persist")
	}
	if s.persisted != 2 {
		t.Errorf("persisted = %d, want 2 (the compacted length)", s.persisted)
	}

	_, msgs, err := store.Load(s.header.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want 2 (flattened compacted context)", len(msgs))
	}
}

func saveSessionWith(t *testing.T, store *session.Store, model, provider string, msgs agentcore.MessageList) string {
	t.Helper()
	now := time.Now().UTC()
	header := session.SessionHeader{
		ID:        session.NewID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     model,
		Provider:  provider,
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return header.ID
}

func userMsg(text string) agentcore.Message {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

// TestSwitchToSession verifies in-TUI session switching: the current session is
// persisted first, the target loads with its history, and the live model
// follows the stored header.
func TestSwitchToSession(t *testing.T) {
	store := newTestStore(t)
	idA := saveSessionWith(t, store, "model-a", "prov", nil)
	idB := saveSessionWith(t, store, "model-b", "prov", agentcore.MessageList{userMsg("hi b")})

	s := &runSession{
		store:    store,
		header:   session.SessionHeader{ID: idA, Model: "model-a", Provider: "prov"},
		agentCtx: &agentcore.AgentContext{Messages: agentcore.MessageList{userMsg("unsaved a")}},
		live:     &cli.LiveConfig{Model: "model-a", ProviderName: "prov"},
	}

	msgs, err := s.switchTo(idB)
	if err != nil {
		t.Fatalf("switchTo: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("loaded msgs = %d, want 1", len(msgs))
	}
	if s.header.ID != idB {
		t.Errorf("header.ID = %q, want %q", s.header.ID, idB)
	}
	if s.live.Model != "model-b" {
		t.Errorf("live.Model = %q, want model-b", s.live.Model)
	}
	if s.persisted != 1 || s.curLeaf == "" {
		t.Errorf("persisted = %d curLeaf = %q, want 1 and non-empty", s.persisted, s.curLeaf)
	}
	if s.hookDeps.SessionID != idB {
		t.Errorf("hookDeps.SessionID = %q, want %q", s.hookDeps.SessionID, idB)
	}
	// The unsaved turn on A must have been persisted before the switch.
	_, aEntries, err := store.LoadEntries(idA)
	if err != nil {
		t.Fatalf("LoadEntries(A): %v", err)
	}
	if len(aEntries) != 1 {
		t.Fatalf("session A should hold the unsaved turn, got %d entries", len(aEntries))
	}
	um, ok := aEntries[0].Message.(agentcore.UserMessage)
	if !ok || agentcore.ContentToText(um.Content) != "unsaved a" {
		t.Errorf("session A entry = %T, want the unsaved user turn", aEntries[0].Message)
	}

	// Switching to the active session and to unknown/empty ids errors.
	if _, err := s.switchTo(idB); err == nil {
		t.Error("switch to active session should error")
	}
	if _, err := s.switchTo(""); err == nil {
		t.Error("switch to empty id should error")
	}
	if _, err := s.switchTo("no-such-session"); err == nil {
		t.Error("switch to unknown id should error")
	}
	if s.header.ID != idB {
		t.Errorf("failed switch must not move header, got %q", s.header.ID)
	}
}

// TestSwitchToFollowsProvider verifies a provider change re-resolves the live
// driver from the registry (no network), while the thinking level is kept.
func TestSwitchToFollowsProvider(t *testing.T) {
	store := newTestStore(t)
	idA := saveSessionWith(t, store, "model-a", "prov", nil)
	idB := saveSessionWith(t, store, "gpt-4o", "openai", nil)

	s := &runSession{
		store:    store,
		header:   session.SessionHeader{ID: idA, Model: "model-a", Provider: "prov"},
		agentCtx: &agentcore.AgentContext{},
		live:     &cli.LiveConfig{Model: "model-a", ProviderName: "prov", ThinkingLevel: "xhigh"},
	}
	if _, err := s.switchTo(idB); err != nil {
		t.Fatalf("switchTo: %v", err)
	}
	if s.live.ProviderName != "openai" || s.live.Model != "gpt-4o" {
		t.Errorf("live = (%q, %q), want (openai, gpt-4o)", s.live.ProviderName, s.live.Model)
	}
	if s.live.Provider == nil {
		t.Error("live.Provider should be re-resolved, got nil")
	}
	if s.live.ThinkingLevel != "xhigh" {
		t.Errorf("thinking level should be kept, got %q", s.live.ThinkingLevel)
	}
}
