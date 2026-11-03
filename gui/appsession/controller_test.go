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
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
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

func enableProjectSwitching(c *Controller) {
	c.mu.Lock()
	c.factory = func(_ context.Context, cwd string, setup ProjectSetup) (run.Env, error) {
		credentials := provider.NewCredentialStore(nil)
		credentials.SetOverride(setup.ProviderName, setup.APIKey)
		prompt := runtime.PromptInputs{WorkingDir: cwd}
		return run.Env{
			Cwd: cwd, ProviderName: setup.ProviderName, Credentials: credentials,
			SysPrompt: "project: " + cwd, Prompt: prompt,
			Permissions: permissions.New(permissions.Auto), ReviewNotes: run.NewReviewNotes(),
			Budget: contextbudget.New(),
		}, nil
	}
	c.mu.Unlock()
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

func TestNewSessionInSelectedProjectRebuildsProjectRuntime(t *testing.T) {
	c, _, launchDir := newTestController(t)
	enableProjectSwitching(c)
	target := t.TempDir()
	project, err := c.AddProject(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.NewSessionInProject(context.Background(), project.Path); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	if snap.Project != project.Path {
		t.Fatalf("project = %q, want %q (launch was %q)", snap.Project, project.Path, launchDir)
	}
	if c.header.Cwd != project.Path || c.prompt.WorkingDir != project.Path {
		t.Fatalf("runtime cwd header=%q prompt=%q", c.header.Cwd, c.prompt.WorkingDir)
	}
	projects, selected, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || selected != project.Path {
		t.Fatalf("projects=%+v selected=%q", projects, selected)
	}
}

func TestRemoveProjectStillWorksAfterFolderIsDeleted(t *testing.T) {
	c, _, _ := newTestController(t)
	target := t.TempDir()
	project, err := c.AddProject(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveProject(project.Path); err != nil {
		t.Fatal(err)
	}
	projects, _, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range projects {
		if candidate.Path == project.Path {
			t.Fatalf("deleted project remains in sidebar: %+v", projects)
		}
	}
}

func TestResumeSwitchesBackToSessionProject(t *testing.T) {
	c, _, launchDir := newTestController(t)
	enableProjectSwitching(c)
	target := t.TempDir()
	project, err := c.AddProject(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.NewSessionInProject(context.Background(), project.Path); err != nil {
		t.Fatal(err)
	}
	targetSession := c.SessionID()
	c.mu.Lock()
	c.agentCtx.Messages = append(c.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("target project")},
	})
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		t.Fatal(err)
	}
	if err := c.NewSessionInProject(context.Background(), launchDir); err != nil {
		t.Fatal(err)
	}
	if err := c.Resume(context.Background(), targetSession); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Project; got != project.Path {
		t.Fatalf("resumed project = %q, want %q", got, project.Path)
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
	diff, err := c.FileDiff(context.Background(), ChangesUnstaged, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Patch, "hello.txt") || diff.Added != 1 {
		t.Fatalf("untracked diff = %+v", diff)
	}
}

func TestChangesSeparatesAndStagesIndexViews(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c, _, dir := newTestController(t)
	runGitCommand := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGitCommand("init", "-q")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand("add", "tracked.txt")
	runGitCommand("commit", "-qm", "base")
	if err := os.WriteFile(tracked, []byte("base\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	unstaged, err := c.ChangesFor(context.Background(), ChangesUnstaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(unstaged.Files) != 2 {
		t.Fatalf("unstaged = %+v", unstaged.Files)
	}
	trackedDiff, err := c.FileDiff(context.Background(), ChangesUnstaged, "tracked.txt")
	if err != nil || !strings.Contains(trackedDiff.Patch, "changed") {
		t.Fatalf("tracked diff = %+v, %v", trackedDiff, err)
	}
	newDiff, err := c.FileDiff(context.Background(), ChangesUnstaged, "new.txt")
	if err != nil || !strings.Contains(newDiff.Patch, "new.txt") {
		t.Fatalf("new-file diff = %+v, %v", newDiff, err)
	}
	if err := c.SetFileStaged(context.Background(), "tracked.txt", true); err != nil {
		t.Fatal(err)
	}
	unstaged, _ = c.ChangesFor(context.Background(), ChangesUnstaged)
	staged, err := c.ChangesFor(context.Background(), ChangesStaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(unstaged.Files) != 1 || unstaged.Files[0].Path != "new.txt" {
		t.Fatalf("unstaged after stage = %+v", unstaged.Files)
	}
	if len(staged.Files) != 1 || staged.Files[0].Path != "tracked.txt" {
		t.Fatalf("staged = %+v", staged.Files)
	}
	stagedDiff, err := c.FileDiff(context.Background(), ChangesStaged, "tracked.txt")
	if err != nil || !strings.Contains(stagedDiff.Patch, "changed") {
		t.Fatalf("staged diff = %+v, %v", stagedDiff, err)
	}
	if err := c.SetAllStaged(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	staged, _ = c.ChangesFor(context.Background(), ChangesStaged)
	if len(staged.Files) != 2 {
		t.Fatalf("stage all = %+v", staged.Files)
	}
	if err := c.SetAllStaged(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	staged, _ = c.ChangesFor(context.Background(), ChangesStaged)
	if len(staged.Files) != 0 {
		t.Fatalf("unstage all left %+v", staged.Files)
	}
}

func TestUnstageRenamedFileIncludesOriginalPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c, _, dir := newTestController(t)
	runGitCommand := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGitCommand("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "before.txt"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand("add", "before.txt")
	runGitCommand("commit", "-qm", "base")
	runGitCommand("mv", "before.txt", "after.txt")
	staged, err := c.ChangesFor(context.Background(), ChangesStaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 1 || staged.Files[0].Path != "after.txt" || staged.Files[0].Status != "R" {
		t.Fatalf("staged rename = %+v", staged.Files)
	}
	if err := c.SetFileStaged(context.Background(), "after.txt", false); err != nil {
		t.Fatal(err)
	}
	staged, err = c.ChangesFor(context.Background(), ChangesStaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 0 {
		t.Fatalf("rename remained staged: %+v", staged.Files)
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

func TestChangesFindsRepositoryAboveProjectFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c, _, root := newTestController(t)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	nested := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.cwd = nested
	c.mu.Unlock()
	changes, err := c.Changes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changes.NotGitRepo || !sameProjectPath(changes.RepoRoot, root) {
		t.Fatalf("changes = %+v, want repo root %q", changes, root)
	}
}

func TestBuildUserAttachmentsIncludesTextAndImage(t *testing.T) {
	textPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(textPath, []byte("hello attachment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "pixel.png")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := os.WriteFile(imagePath, png, 0o600); err != nil {
		t.Fatal(err)
	}
	content, display, err := buildUserAttachments("review these", []UserAttachment{{Path: textPath}, {Path: imagePath}})
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 3 || !strings.Contains(display, "notes.txt") || !strings.Contains(display, "pixel.png") {
		t.Fatalf("content=%#v display=%q", content, display)
	}
	if _, ok := content[2].(agentcore.ImageContent); !ok {
		t.Fatalf("image block = %T", content[2])
	}
}

func TestBuildUserAttachmentsRejectsOversizedFileBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxImageAttachmentBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildUserAttachments("", []UserAttachment{{Path: path}}); err == nil || !strings.Contains(err.Error(), "20 MiB") {
		t.Fatalf("oversized attachment error = %v", err)
	}
}

func TestBoundedBufferConsumesAndLimitsOutput(t *testing.T) {
	buffer := boundedBuffer{limit: 4}
	written, err := buffer.Write([]byte("abcdefghij"))
	if err != nil {
		t.Fatal(err)
	}
	if written != 10 || buffer.String() != "abcd" {
		t.Fatalf("written=%d buffer=%q", written, buffer.String())
	}
	written, err = buffer.Write([]byte("more"))
	if err != nil || written != 4 || buffer.String() != "abcd" {
		t.Fatalf("second write=%d err=%v buffer=%q", written, err, buffer.String())
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
