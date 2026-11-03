package appsession

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

func newTestController(t *testing.T) (*Controller, *session.Store, string) {
	t.Helper()
	t.Setenv("GOLDER_HOME", t.TempDir())
	dir := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := New(Options{
		Model:        "test-model",
		ProviderName: "test-provider",
		Cwd:          dir,
		Store:        store,
	})
	if err := c.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, store, dir
}

func TestOpenSnapshot(t *testing.T) {
	c, _, dir := newTestController(t)
	snap := c.Snapshot()
	if snap.SessionID == "" {
		t.Fatal("snapshot has no session id")
	}
	if snap.Model != "test-model" || snap.Provider != "test-provider" {
		t.Fatalf("live config = %q/%q", snap.Provider, snap.Model)
	}
	if snap.Permission != "auto" {
		t.Fatalf("permission = %q, want auto", snap.Permission)
	}
	if snap.Project != dir {
		t.Fatalf("project = %q, want %q", snap.Project, dir)
	}
	if snap.Title == "" {
		t.Fatal("title is empty")
	}
}

func TestSessionLockRefusesSecondWriter(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireSessionLock(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	if _, err := acquireSessionLock(dir, "s1"); err == nil {
		t.Fatal("second lock succeeded")
	}
	first.release()
	second, err := acquireSessionLock(dir, "s1")
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	second.release()
}

func TestStaleLockIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.lock")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lk, err := acquireSessionLock(dir, "old")
	if err != nil {
		t.Fatalf("stale lock was not reclaimed: %v", err)
	}
	lk.release()
}

func TestPermissionModeSwitch(t *testing.T) {
	c, _, _ := newTestController(t)
	ctx := context.Background()
	if err := c.SetPermissionMode(ctx, "ask"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Permission; got != "ask" {
		t.Fatalf("permission = %q, want ask", got)
	}
	if err := c.SetPermissionMode(ctx, "nope"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestNewSessionAndResume(t *testing.T) {
	c, store, _ := newTestController(t)
	first := c.SessionID()

	if err := c.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := c.SessionID()
	if second == first {
		t.Fatal("new session reused the old id")
	}

	// Writing a user message and persisting it makes the session resumable.
	c.mu.Lock()
	c.agentCtx.Messages = append(c.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent("resume me")},
	})
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		t.Fatal(err)
	}

	if err := c.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Resume(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	if snap.SessionID != second {
		t.Fatalf("resumed %q, want %q", snap.SessionID, second)
	}
	if len(snap.Items) != 1 || snap.Items[0].Text != "resume me" {
		t.Fatalf("transcript not seeded: %+v", snap.Items)
	}
	if _, entries, err := store.LoadEntries(second); err != nil || len(entries) != 1 {
		t.Fatalf("stored entries = %d, err %v", len(entries), err)
	}
}

func TestListSessionsMarksCurrent(t *testing.T) {
	c, _, _ := newTestController(t)
	sessions, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if !sessions[0].Current {
		t.Fatal("current session is not marked")
	}
}

func TestActionsExposeGuiAndSlashCommands(t *testing.T) {
	c, _, _ := newTestController(t)
	actions := c.ListActions(context.Background())
	ids := map[string]bool{}
	for _, a := range actions {
		ids[a.ID] = true
	}
	if !ids[ActionNewSession] || !ids[ActionCompact] {
		t.Fatalf("GUI actions missing: %v", ids)
	}
	if !ids["model"] && !ids["help"] {
		t.Fatalf("slash commands missing from the catalog: %v", ids)
	}
}

func TestChangesOnGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c, _, dir := newTestController(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ch, err := c.Changes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ch.NotGitRepo {
		t.Fatal("repository reported as not Git")
	}
	if len(ch.Files) != 1 || ch.Files[0].Path != "hello.txt" {
		t.Fatalf("files = %+v", ch.Files)
	}
}

func TestChangesOutsideRepositoryIsNotAnError(t *testing.T) {
	c, _, _ := newTestController(t)
	ch, err := c.Changes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ch.NotGitRepo {
		t.Fatal("a plain directory should report NotGitRepo")
	}
}

func TestSubmitRejectsEmptyPrompt(t *testing.T) {
	c, _, _ := newTestController(t)
	if err := c.Submit(context.Background(), "   "); err != nil {
		t.Fatalf("empty prompt: %v", err)
	}
	if c.IsRunning() {
		t.Fatal("empty prompt started a run")
	}
}

func TestTitleOfFirstUserMessage(t *testing.T) {
	items := []Item{
		{Kind: ItemAssistant, Text: "hello"},
		{Kind: ItemUser, Text: "Fix the login flow\nand tests"},
	}
	if got := titleOf(items); got != "Fix the login flow" {
		t.Fatalf("title = %q", got)
	}
}

func TestApprovalViewKeepsStructuredFields(t *testing.T) {
	if got := approvalKey(judgeRequest("bash", "rm -rf build")); strings.Contains(got, "rm -rf build") == false {
		t.Fatalf("approval key = %q", got)
	}
}
