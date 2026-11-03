package appsession

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxPatchBytes bounds the diff handed to the view; a huge patch is truncated
// rather than loaded into the window.
const maxPatchBytes = 2 << 20

// Changes returns the project's uncommitted work: a status list and a unified
// patch. A directory that is not a Git repository reports so instead of an
// error, because the drawer is always available.
func (c *Controller) Changes(ctx context.Context) (Changes, error) {
	c.mu.Lock()
	cwd := c.cwd
	c.mu.Unlock()
	if cwd == "" {
		return Changes{NotGitRepo: true}, nil
	}
	if _, err := runGit(ctx, cwd, "rev-parse", "--is-inside-work-tree"); err != nil {
		return Changes{NotGitRepo: true}, nil
	}

	out := Changes{}
	status, err := runGit(ctx, cwd, "status", "--porcelain=v1", "-z")
	if err != nil {
		return out, err
	}
	for _, rec := range strings.Split(status, "\x00") {
		if len(rec) < 4 {
			continue
		}
		code, path := rec[:2], rec[3:]
		out.Files = append(out.Files, FileStatus{Path: path, Status: strings.TrimSpace(code)})
	}

	// HEAD may not exist yet (a repository without commits); the diff against
	// the empty tree still shows staged work.
	diff, err := runGitBounded(ctx, cwd, maxPatchBytes, "diff", "--no-color", "--unified=3", "HEAD")
	if err != nil {
		diff, err = runGitBounded(ctx, cwd, maxPatchBytes, "diff", "--no-color", "--unified=3")
		if err != nil {
			return out, err
		}
	}
	if diff == "" {
		if cached, cerr := runGitBounded(ctx, cwd, maxPatchBytes, "diff", "--no-color", "--cached", "--unified=3"); cerr == nil {
			diff = cached
		}
	}
	out.Patch = diff
	if len(diff) >= maxPatchBytes {
		out.Truncated = true
	}
	out.Added, out.Deleted = countDiffLines(diff)
	return out, nil
}

// ChangedFiles returns just the file list, for a cheap status refresh.
func (c *Controller) ChangedFiles(ctx context.Context) ([]FileStatus, error) {
	ch, err := c.Changes(ctx)
	if err != nil {
		return nil, err
	}
	return ch.Files, nil
}

func countDiffLines(patch string) (added, deleted int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			deleted++
		}
	}
	return added, deleted
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitBounded(ctx, dir, 1<<20, args...)
}

func runGitBounded(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never let a repository-level credential helper or pager run.
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), err
	}
	out := stdout.String()
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// relPath renders a path relative to the project for display.
func relPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}

// timeNowMillis is the compaction checkpoint stamp.
func timeNowMillis() int64 { return time.Now().UnixMilli() }

// ParseDiffStat reads the summary line git prints after a diff, if present.
func ParseDiffStat(patch string) (files, added, deleted int) {
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, " ") || !strings.Contains(line, "changed") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			files, _ = strconv.Atoi(fields[0])
		}
	}
	return files, added, deleted
}
