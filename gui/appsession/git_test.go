package appsession

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func operatorGitFixture(t *testing.T) (*Controller, string, GitRequest) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for key, value := range map[string]string{
		"GIT_AUTHOR_NAME": "GUI test", "GIT_AUTHOR_EMAIL": "gui@example.test",
		"GIT_COMMITTER_NAME": "GUI test", "GIT_COMMITTER_EMAIL": "gui@example.test",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(key, value)
	}
	c, _, dir := newTestController(t)
	gitFixture(t, dir, "init", "-q", "-b", "main")
	ch, err := c.Changes(context.Background())
	if err != nil || ch.NotGitRepo {
		t.Fatalf("status: %+v %v", ch, err)
	}
	return c, dir, GitRequest{SessionID: c.SessionID(), Project: ch.Project, RepoRoot: ch.RepoRoot}
}

func writeGitFixture(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorGitCommitsOnlyStagedFiles(t *testing.T) {
	c, dir, request := operatorGitFixture(t)
	writeGitFixture(t, dir, "one.txt", "one\n")
	writeGitFixture(t, dir, "two.txt", "two\n")
	request.Action, request.Path = "stage", "one.txt"
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	ch, _ := c.Changes(context.Background())
	if ch.StagedFiles != 1 {
		t.Fatalf("staged count = %d", ch.StagedFiles)
	}
	request.Action, request.Message = "commit", "first commit\n\nOnly selected changes."
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := gitFixture(t, dir, "ls-tree", "--name-only", "HEAD"); got != "one.txt" {
		t.Fatalf("commit auto-staged changes: %q", got)
	}
	if _, err := c.GitOperation(context.Background(), request); err == nil {
		t.Fatal("empty staged commit succeeded")
	}
	request.Action, request.Path = "stage", ""
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Action = "unstage"
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "two.txt")); err != nil {
		t.Fatal("unstage modified working tree")
	}
}

func TestOperatorGitPushFetchPullUsesLocalRemoteAndRefusesDivergence(t *testing.T) {
	c, dir, request := operatorGitFixture(t)
	writeGitFixture(t, dir, "one.txt", "initial\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	remote := t.TempDir()
	gitFixture(t, remote, "init", "--bare", "-q", "-b", "main")
	gitFixture(t, dir, "remote", "add", "origin", remote)
	gitFixture(t, dir, "push", "-q", "-u", "origin", "main")
	request.Action = "push"
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	peer := t.TempDir()
	gitFixture(t, peer, "clone", "-q", remote, ".")
	writeGitFixture(t, peer, "remote.txt", "from remote\n")
	gitFixture(t, peer, "add", ".")
	gitFixture(t, peer, "-c", "commit.gpgsign=false", "commit", "-qm", "remote commit")
	gitFixture(t, peer, "push", "-q")
	request.Action = "fetch"
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	ch, err := c.Changes(context.Background())
	if err != nil || ch.Behind != 1 || ch.Ahead != 0 || ch.Upstream != "origin/main" {
		t.Fatalf("upstream status: %+v %v", ch, err)
	}
	request.Action = "pull"
	if _, err := c.GitOperation(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "remote.txt")); err != nil {
		t.Fatal("pull did not fast-forward worktree")
	}
	writeGitFixture(t, dir, "local.txt", "local divergence\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "-c", "commit.gpgsign=false", "commit", "-qm", "local divergence")
	writeGitFixture(t, peer, "remote.txt", "remote divergence\n")
	gitFixture(t, peer, "add", ".")
	gitFixture(t, peer, "-c", "commit.gpgsign=false", "commit", "-qm", "remote divergence")
	gitFixture(t, peer, "push", "-q")
	head := gitFixture(t, dir, "rev-parse", "HEAD")
	if _, err := c.GitOperation(context.Background(), request); err == nil || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("divergent pull: %v", err)
	}
	if gitFixture(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("pull merged or rewrote local history")
	}
}

func TestOperatorGitRejectsForeignAndBusyConversations(t *testing.T) {
	c, _, request := operatorGitFixture(t)
	request.Action = "stage"
	bad := request
	bad.SessionID = "other-conversation"
	if _, err := c.GitOperation(context.Background(), bad); err == nil {
		t.Fatal("accepted foreign conversation")
	}
	bad = request
	bad.RepoRoot = t.TempDir()
	if _, err := c.GitOperation(context.Background(), bad); err == nil {
		t.Fatal("accepted foreign repository")
	}
	c.running = true
	if _, err := c.GitOperation(context.Background(), request); !errors.Is(err, ErrBusy) {
		t.Fatalf("running: %v", err)
	}
	c.running, c.gitMutation = false, true
	if err := c.Submit(context.Background(), "must not start"); err == nil || !strings.Contains(err.Error(), "Git operation") {
		t.Fatalf("agent started during Git operation: %v", err)
	}
	c.gitMutation, c.readOnly = false, true
	if _, err := c.GitOperation(context.Background(), request); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only: %v", err)
	}
	c.readOnly = false
}
