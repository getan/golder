package appsession

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getan/golder/internal/permissions"
)

// GitRequest is an explicit local operator action, never an Agent tool. Scope
// is captured from the visible conversation/repository before async dispatch.
type GitRequest struct {
	SessionID string
	Project   string
	RepoRoot  string
	Action    string
	Message   string
	Path      string
}

// GitOperation serializes operator mutations and blocks an Agent turn from
// starting until completion. It never force-pushes, rebases or implicitly stages
// a commit. Pull is fast-forward-only and failures are not automatically retried.
func (c *Controller) GitOperation(ctx context.Context, request GitRequest) (string, error) {
	c.mu.Lock()
	switch {
	case c.closed:
		c.mu.Unlock()
		return "", ErrClosed
	case c.readOnly || c.perms != nil && c.perms.Mode() == permissions.ReadOnly:
		c.mu.Unlock()
		return "", ErrReadOnly
	case c.running || c.gitMutation || c.privateAuthorization || c.extensionMutation:
		c.mu.Unlock()
		return "", ErrBusy
	case request.SessionID == "" || request.SessionID != c.header.ID || filepath.Clean(request.Project) != filepath.Clean(c.cwd):
		c.mu.Unlock()
		return "", errors.New("conversation changed; refresh Git before continuing")
	}
	c.gitMutation = true
	done := c.done
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.gitMutation = false; c.mu.Unlock() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	root, err := c.repoRoot(ctx)
	if err != nil {
		return "", err
	}
	if request.RepoRoot == "" || filepath.Clean(root) != filepath.Clean(request.RepoRoot) {
		return "", errors.New("repository changed; refresh Git before continuing")
	}
	var args []string
	stdin := ""
	switch request.Action {
	case "commit":
		if strings.TrimSpace(request.Message) == "" || len(request.Message) > 64<<10 || !utf8.ValidString(request.Message) || strings.ContainsRune(request.Message, '\x00') {
			return "", errors.New("enter a valid commit message")
		}
		if staged, err := runGit(ctx, root, "diff", "--cached", "--name-only"); err != nil || strings.TrimSpace(staged) == "" {
			return "", errors.New("no staged changes to commit; stage files first")
		}
		args, stdin = []string{"commit", "--file=-"}, request.Message
	case "push":
		args = []string{"push"}
	case "pull":
		args = []string{"pull", "--ff-only"}
	case "fetch":
		args = []string{"fetch"}
	case "stage", "unstage":
		path := filepath.Clean(request.Path)
		if request.Path == "" {
			path = "."
		}
		if path != "." && !validRepositoryPath(path) {
			return "", errors.New("invalid Git path")
		}
		paths := []string{path}
		if path != "." {
			paths = c.statusPaths(ctx, root, path)
		}
		if request.Action == "unstage" {
			return "Unstaged without changing working files.", unstage(ctx, root, paths...)
		}
		args = append([]string{"add", "-A", "--"}, paths...)
	default:
		return "", errors.New("unsupported Git operation")
	}
	return runGitOperator(ctx, root, stdin, args...)
}

func runGitOperator(ctx context.Context, root, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	// Interactive credential acquisition belongs in the user's terminal. Keep
	// normal configured credential helpers/hooks, but never request a GUI secret.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never", "GIT_LITERAL_PATHSPECS=1")
	cmd.WaitDelay = 2 * time.Second
	configureOperatorGit(cmd)
	cmd.Stdin = strings.NewReader(input)
	output := boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return output.String(), fmt.Errorf("Git operation interrupted; inspect repository state before retrying: %w", ctx.Err())
		}
		return output.String(), fmt.Errorf("git %s failed: %s (%w)", args[0], strings.TrimSpace(output.String()), err)
	}
	text := strings.TrimSpace(output.String())
	if text == "" {
		text = "Git " + args[0] + " completed."
	}
	return text, nil
}
