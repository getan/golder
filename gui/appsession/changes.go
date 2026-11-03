package appsession

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// maxPatchBytes bounds the diff handed to the view and the subprocess output
	// retained in memory.
	maxPatchBytes = 2 << 20
	// The file list is useful well before an enormous generated tree is. Keep
	// status bounded and let the user narrow the repository outside the GUI.
	maxGitStatusBytes = 4 << 20
	maxChangedFiles   = 2000
)

// Changes returns the project's unstaged status list. File patches are loaded
// separately through FileDiff so opening the drawer never builds every diff.
// A directory that is not a Git repository reports so instead of an error,
// because the drawer is always available.
func (c *Controller) Changes(ctx context.Context) (Changes, error) {
	return c.ChangesFor(ctx, ChangesUnstaged)
}

// ChangesFor returns either working-tree (unstaged) or index (staged) changes.
// Keeping those views separate mirrors Git and prevents a combined HEAD diff
// from making a file look accepted when it still has unstaged edits.
func (c *Controller) ChangesFor(ctx context.Context, view ChangeView) (Changes, error) {
	if view != ChangesStaged {
		view = ChangesUnstaged
	}
	c.mu.Lock()
	cwd := c.cwd
	c.mu.Unlock()
	if cwd == "" {
		return Changes{View: view, NotGitRepo: true}, nil
	}
	repoRoot, err := runGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return Changes{Project: cwd, View: view, NotGitRepo: true}, nil
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return Changes{Project: cwd, View: view, NotGitRepo: true}, nil
	}

	out := Changes{Project: cwd, RepoRoot: repoRoot, View: view}
	if branch, branchErr := runGit(ctx, repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD"); branchErr == nil {
		out.Branch = strings.TrimSpace(branch)
	} else if commit, commitErr := runGit(ctx, repoRoot, "rev-parse", "--short", "HEAD"); commitErr == nil {
		out.Branch = strings.TrimSpace(commit)
	}
	status, err := runGitBounded(ctx, repoRoot, maxGitStatusBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return out, err
	}
	out.TotalFiles = statusFileCount(status)
	out.Files = parseStatusView(status, view)
	if len(status) >= maxGitStatusBytes || len(out.Files) > maxChangedFiles {
		out.Truncated = true
	}
	if len(out.Files) > maxChangedFiles {
		out.Files = out.Files[:maxChangedFiles]
	}
	numstatArgs := []string{"diff", "--numstat", "-z"}
	if view == ChangesStaged {
		numstatArgs = append(numstatArgs, "--cached")
	}
	stats := map[string][2]int{}
	if numstat, statErr := runGit(ctx, repoRoot, numstatArgs...); statErr == nil {
		stats = parseNumstat(numstat)
	}
	for i := range out.Files {
		if stat, ok := stats[out.Files[i].Path]; ok {
			out.Files[i].Added, out.Files[i].Deleted = stat[0], stat[1]
		}
		out.Added += out.Files[i].Added
		out.Deleted += out.Files[i].Deleted
	}
	return out, nil
}

// FileDiff lazily loads one file only. It never builds a repository-wide patch,
// and the subprocess writer discards bytes beyond maxPatchBytes.
func (c *Controller) FileDiff(ctx context.Context, view ChangeView, path string) (FileDiff, error) {
	if view != ChangesStaged {
		view = ChangesUnstaged
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if !validRepositoryPath(path) {
		return FileDiff{}, errors.New("invalid repository path")
	}
	repoRoot, err := c.repoRoot(ctx)
	if err != nil {
		return FileDiff{}, err
	}
	var patch string
	if view == ChangesUnstaged {
		if _, trackedErr := runGit(ctx, repoRoot, "ls-files", "--error-unmatch", "--", path); trackedErr != nil {
			if ctx.Err() != nil {
				return FileDiff{}, ctx.Err()
			}
			patch, err = runGitNoIndex(ctx, repoRoot, maxPatchBytes, path)
		} else {
			paths := c.statusPaths(ctx, repoRoot, path)
			args := append([]string{"diff", "--no-color", "--unified=3", "--"}, paths...)
			patch, err = runGitBounded(ctx, repoRoot, maxPatchBytes, args...)
		}
	} else {
		paths := c.statusPaths(ctx, repoRoot, path)
		args := append([]string{"diff", "--no-color", "--unified=3", "--cached", "--"}, paths...)
		patch, err = runGitBounded(ctx, repoRoot, maxPatchBytes, args...)
	}
	if err != nil {
		return FileDiff{}, err
	}
	added, deleted := countDiffLines(patch)
	return FileDiff{Path: path, Patch: patch, Added: added, Deleted: deleted, Truncated: len(patch) >= maxPatchBytes}, nil
}

func parseStatusView(status string, view ChangeView) []FileStatus {
	records := strings.Split(status, "\x00")
	files := make([]FileStatus, 0, len(records))
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		code, path := record[:2], record[3:]
		if code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C' {
			// Porcelain -z emits the old path as the next NUL record. The first
			// record carries the destination, which is the actionable path.
			i++
		}
		var state byte
		if view == ChangesStaged {
			if code[0] != ' ' && code[0] != '?' {
				state = code[0]
			}
		} else if code == "??" {
			state = '?'
		} else if code[1] != ' ' {
			state = code[1]
		}
		if state != 0 {
			files = append(files, FileStatus{Path: path, Status: string(state)})
		}
	}
	return files
}

func statusFileCount(status string) int {
	records := strings.Split(status, "\x00")
	count := 0
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		count++
		code := record[:2]
		if code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C' {
			i++
		}
	}
	return count
}

func parseNumstat(numstat string) map[string][2]int {
	stats := map[string][2]int{}
	records := strings.Split(numstat, "\x00")
	for i := 0; i < len(records); i++ {
		fields := strings.SplitN(records[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := fields[2]
		if path == "" && i+2 < len(records) {
			path = records[i+2]
			i += 2
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		stats[path] = [2]int{added, deleted}
	}
	return stats
}

func runGitNoIndex(ctx context.Context, repoRoot string, limit int, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-color", "--unified=3", "--", os.DevNull, path)
	cmd.Dir = repoRoot
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	stdout := boundedBuffer{limit: limit}
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return "", err
	}
	return stdout.String(), nil
}

// SetAllStaged stages or unstages every path in the active repository without
// modifying working-tree contents.
func (c *Controller) SetAllStaged(ctx context.Context, staged bool) error {
	repoRoot, err := c.repoRoot(ctx)
	if err != nil {
		return err
	}
	if staged {
		_, err = runGit(ctx, repoRoot, "add", "-A", "--", ".")
		return err
	}
	return unstage(ctx, repoRoot, ".")
}

// SetFileStaged stages or unstages one status path from the active repository.
func (c *Controller) SetFileStaged(ctx context.Context, path string, staged bool) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !validRepositoryPath(path) {
		return errors.New("invalid repository path")
	}
	repoRoot, err := c.repoRoot(ctx)
	if err != nil {
		return err
	}
	paths := c.statusPaths(ctx, repoRoot, path)
	if staged {
		args := append([]string{"add", "-A", "--"}, paths...)
		_, err = runGit(ctx, repoRoot, args...)
		return err
	}
	return unstage(ctx, repoRoot, paths...)
}

func (c *Controller) statusPaths(ctx context.Context, repoRoot, path string) []string {
	status, err := runGit(ctx, repoRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return []string{path}
	}
	records := strings.Split(status, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		code, current := record[:2], record[3:]
		renamed := code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C'
		if renamed && i+1 < len(records) {
			original := filepath.Clean(records[i+1])
			i++
			if current == path && validRepositoryPath(original) {
				return []string{path, original}
			}
		}
	}
	return []string{path}
}

func validRepositoryPath(path string) bool {
	return path != "." && !filepath.IsAbs(path) && path != ".." &&
		!strings.HasPrefix(path, ".."+string(filepath.Separator)) && strings.IndexByte(path, 0) < 0
}

func (c *Controller) repoRoot(ctx context.Context) (string, error) {
	c.mu.Lock()
	cwd := c.cwd
	c.mu.Unlock()
	root, err := runGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		return "", errors.New("current project is not a Git repository")
	}
	return strings.TrimSpace(root), nil
}

func unstage(ctx context.Context, repoRoot string, paths ...string) error {
	if _, err := runGit(ctx, repoRoot, "rev-parse", "--verify", "HEAD"); err == nil {
		args := append([]string{"reset", "-q", "HEAD", "--"}, paths...)
		_, err = runGit(ctx, repoRoot, args...)
		return err
	}
	args := append([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch", "--"}, paths...)
	_, err := runGit(ctx, repoRoot, args...)
	return err
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
	stdout := boundedBuffer{limit: limit}
	stderr := boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), err
	}
	return stdout.String(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	// Report the full input consumed so os/exec keeps draining the pipe while
	// bytes beyond the UI limit are discarded.
	return original, nil
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
