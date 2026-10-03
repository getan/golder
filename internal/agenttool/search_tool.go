// This file implements the search tools (US-019): grep (search file contents by
// regexp with optional glob filtering), find (locate files by name glob), and ls
// (list a directory, distinguishing files from directories).
//
// grep and find delegate to ripgrep (rg) — the tool codex's prompt tells the
// model to prefer. rg is multithreaded, skips binary/hidden/.gitignore'd paths,
// and is several times faster than a walk-and-scan; delegating also deletes the
// hand-rolled walker and .gitignore matcher this file used to carry. ls stays
// pure Go (a single directory read). All three resolve paths against a Root
// with the same boundary guard as the other tools, and the read-only search
// tools run parallel.
package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// searchMaxResults caps the number of matches/entries any single search returns
// so a broad query cannot flood the model's context.
const searchMaxResults = 1000

// resolveWithin resolves p against root and verifies it stays within it. It is
// the single workspace-boundary policy shared by every file tool: the search
// tools call it directly, and ReadTool/WriteTool/EditTool.resolvePath delegate
// to it, so the path-traversal guard lives in exactly one place.
func resolveWithin(root, p string) (string, error) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		root = wd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("invalid root: %w", err)
	}
	var full string
	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Join(absRoot, p)
	}
	rel, err := filepath.Rel(absRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace root", p)
	}
	return full, nil
}

// resolveWithinAny resolves p against the first root that contains it, trying
// roots in order. It exists so the file tools can additionally permit trusted
// out-of-workspace roots (the skills directory) whose absolute SKILL.md paths
// golder itself advertises in the system prompt: without this, the workspace guard
// would reject the very paths the model is instructed to read or author. Empty
// roots are skipped; if none contain p, the standard workspace-escape error is
// returned.
func resolveWithinAny(roots []string, p string) (string, error) {
	var lastErr error
	for _, root := range roots {
		if root == "" {
			continue
		}
		full, err := resolveWithin(root, p)
		if err == nil {
			return full, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	// No usable roots supplied: fall back to the default (cwd) policy.
	return resolveWithin("", p)
}

// rgMaxOutputBytes bounds how much stdout ripgrep may hand back. A broad query
// over a large repo can match far more than the model should see, so the
// capture is capped and the result marked truncated; the cap is generous
// enough to hold searchMaxResults match lines in the common case.
const rgMaxOutputBytes = 4 << 20

// rgLookPath resolves the ripgrep binary on PATH. It is a package var so tests
// can simulate a machine without rg.
var rgLookPath = exec.LookPath

// cappedBuffer accepts writes up to max bytes and silently discards the rest.
// Discarding (rather than blocking) matters: ripgrep writes into this buffer
// from the same process, and a full pipe would deadlock both sides. dropped
// records that the cap was hit so the result can say so.
type cappedBuffer struct {
	buf     bytes.Buffer
	max     int
	dropped bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.dropped = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.dropped = true
	}
	return len(p), nil
}

// rgResult is one completed ripgrep invocation: its stdout lines in rg's order
// plus whether the byte cap cut the output short.
type rgResult struct {
	lines     []string
	truncated bool
}

// runRG executes ripgrep in dir and returns its stdout lines. The second return
// value is a user-facing failure message ("" on success): rg missing from PATH,
// a bad regexp or unreadable path (rg exit 2), or a cancelled run. Exit 1 is
// rg's "no matches" and is a successful empty result, not an error.
//
// The flags every caller shares:
//   - --no-config: ignore RIPGREP_CONFIG_PATH so the output format the caller
//     parses cannot be rewritten by user config;
//   - --no-require-git: honor .gitignore even outside a git checkout (a
//     workspace root need not be a repository);
//   - --no-ignore-parent/--no-ignore-global: stay inside the workspace's own
//     ignore rules instead of inheriting the machine's;
//   - --color=never: the capture is plain text.
func runRG(ctx context.Context, dir string, args []string) (rgResult, string) {
	bin, err := rgLookPath("rg")
	if err != nil {
		return rgResult{}, "ripgrep (rg) not found on PATH; install it (macOS: brew install ripgrep, Debian/Ubuntu: apt install ripgrep)"
	}
	base := []string{
		"--no-config", "--no-require-git", "--no-ignore-parent",
		"--no-ignore-global", "--color=never",
	}
	cmd := exec.CommandContext(ctx, bin, append(base, args...)...)
	cmd.Dir = dir
	// If ripgrep ignores the cancel and keeps writing, WaitDelay tears the
	// process down instead of hanging the tool call forever.
	cmd.WaitDelay = 2 * time.Second
	out := &cappedBuffer{max: rgMaxOutputBytes}
	var errBuf bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return rgResult{}, "search canceled"
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 1: // no matches
				return rgResult{}, ""
			case 2:
				return rgResult{}, "rg: " + firstNonEmptyLine(errBuf.String())
			}
		}
		return rgResult{}, fmt.Sprintf("rg failed: %v", err)
	}
	return rgResult{lines: splitRGOutput(out), truncated: out.dropped}, ""
}

// splitRGOutput splits captured stdout into non-empty lines. When the byte cap
// was hit the final line may be cut mid-line, so it is dropped.
func splitRGOutput(out *cappedBuffer) []string {
	s := out.buf.String()
	if out.dropped {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		} else {
			s = ""
		}
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	raw := strings.Split(s, "\n")
	lines := make([]string, 0, len(raw))
	for _, ln := range raw {
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}

// firstNonEmptyLine returns the first non-blank line of s, for surfacing
// ripgrep's own error text (its regex errors are multi-line).
func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return "unknown error"
}

// rgSearchPath renders the path argument for rg. When the search covers the
// whole root it passes no path at all: rg then prints paths exactly as these
// tools always have (relative to the root, no "./" prefix). `--` guards an
// entry whose name looks like a flag.
func rgSearchPath(rel string) []string {
	if rel == "" || rel == "." {
		return nil
	}
	return []string{"--", rel}
}

// truncationMarker renders the trailing note for a capped result set.
func truncationMarker(unit string, shown int) string {
	if shown >= searchMaxResults {
		return fmt.Sprintf("[truncated at %d %s]", searchMaxResults, unit)
	}
	return "[output truncated]"
}

// GrepTool searches file contents by regexp under Root via ripgrep.
type GrepTool struct {
	// Root bounds the search; empty defaults to the current working directory.
	Root string
}

type grepToolArgs struct {
	// Pattern is the regexp to search for (Go regexp syntax).
	Pattern string `json:"pattern"`
	// Path optionally scopes the search to a subdirectory (relative to Root).
	Path string `json:"path,omitempty"`
	// Glob optionally filters files by base-name glob (e.g. "*.go").
	Glob string `json:"glob,omitempty"`
}

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Search file contents by regular expression under the workspace " +
		"(ripgrep), optionally filtering files by glob. Skips .gitignore'd, " +
		"hidden, and binary files."
}
func (t *GrepTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for."},
    "path":    {"type": "string", "description": "Subdirectory to scope the search to (relative to the workspace root)."},
    "glob":    {"type": "string", "description": "Filter files by base-name glob, e.g. *.go."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)
}

func (t *GrepTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[grepToolArgs](args, "grep")
	if bad != nil {
		return *bad, nil
	}
	if a.Pattern == "" {
		return errorResult("grep: pattern is required"), nil
	}
	// Validate the pattern locally first, so a malformed regexp fails with the
	// same short message as before instead of ripgrep's multi-line regex dump.
	if _, err := regexp.Compile(a.Pattern); err != nil {
		return errorResult(fmt.Sprintf("grep: invalid pattern: %v", err)), nil
	}
	root, err := resolveWithin(t.Root, "")
	if err != nil {
		return errorResult("grep: " + err.Error()), nil
	}
	start := root
	if a.Path != "" {
		if start, err = resolveWithin(t.Root, a.Path); err != nil {
			return errorResult("grep: " + err.Error()), nil
		}
	}
	rel, err := filepath.Rel(root, start)
	if err != nil {
		return errorResult(fmt.Sprintf("grep: %v", err)), nil
	}

	rgArgs := []string{"--line-number", "--no-heading", "--with-filename", "--regexp", a.Pattern}
	if a.Glob != "" {
		rgArgs = append(rgArgs, "--glob="+a.Glob)
	}
	rgArgs = append(rgArgs, rgSearchPath(rel)...)

	res, rgMsg := runRG(ctx, root, rgArgs)
	if rgMsg != "" {
		return errorResult("grep: " + rgMsg), nil
	}
	matches := res.lines
	truncated := res.truncated
	if len(matches) > searchMaxResults {
		matches = matches[:searchMaxResults]
		truncated = true
	}
	msg := fmt.Sprintf("%d match(es) for %q", len(matches), a.Pattern)
	if len(matches) > 0 {
		msg += "\n" + strings.Join(matches, "\n")
	}
	if truncated {
		msg += "\n" + truncationMarker("matches", len(matches))
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"matches": len(matches)},
	}, nil
}

// FindTool locates files by base-name glob under Root via ripgrep.
type FindTool struct {
	// Root bounds the search; empty defaults to the current working directory.
	Root string
}

type findToolArgs struct {
	// Glob is the base-name glob to match (e.g. "*.go").
	Glob string `json:"glob"`
	// Path optionally scopes the search to a subdirectory (relative to Root).
	Path string `json:"path,omitempty"`
}

func (t *FindTool) Name() string { return "find" }
func (t *FindTool) Description() string {
	return "Find files by base-name glob under the workspace (ripgrep). " +
		"Skips .gitignore'd and hidden files."
}
func (t *FindTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}
func (t *FindTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "glob": {"type": "string", "description": "Base-name glob to match, e.g. *.go."},
    "path": {"type": "string", "description": "Subdirectory to scope the search to (relative to the workspace root)."}
  },
  "required": ["glob"],
  "additionalProperties": false
}`)
}

func (t *FindTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[findToolArgs](args, "find")
	if bad != nil {
		return *bad, nil
	}
	if a.Glob == "" {
		return errorResult("find: glob is required"), nil
	}
	root, err := resolveWithin(t.Root, "")
	if err != nil {
		return errorResult("find: " + err.Error()), nil
	}
	start := root
	if a.Path != "" {
		if start, err = resolveWithin(t.Root, a.Path); err != nil {
			return errorResult("find: " + err.Error()), nil
		}
	}
	rel, err := filepath.Rel(root, start)
	if err != nil {
		return errorResult(fmt.Sprintf("find: %v", err)), nil
	}

	rgArgs := []string{"--files", "--glob=" + a.Glob}
	rgArgs = append(rgArgs, rgSearchPath(rel)...)

	res, rgMsg := runRG(ctx, root, rgArgs)
	if rgMsg != "" {
		return errorResult("find: " + rgMsg), nil
	}
	found := res.lines
	sort.Strings(found)
	truncated := res.truncated
	if len(found) > searchMaxResults {
		found = found[:searchMaxResults]
		truncated = true
	}

	msg := fmt.Sprintf("%d file(s) matching %q", len(found), a.Glob)
	if len(found) > 0 {
		msg += "\n" + strings.Join(found, "\n")
	}
	if truncated {
		msg += "\n" + truncationMarker("files", len(found))
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"count": len(found)},
	}, nil
}

// LsTool lists the entries of a directory, distinguishing files from directories.
type LsTool struct {
	// Root bounds the listing; empty defaults to the current working directory.
	Root string
}

type lsToolArgs struct {
	// Path is the directory to list, relative to Root (empty = Root itself).
	Path string `json:"path,omitempty"`
}

func (t *LsTool) Name() string { return "ls" }
func (t *LsTool) Description() string {
	return "List a directory's entries, marking directories with a trailing slash."
}
func (t *LsTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (t *LsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory to list, relative to the workspace root."}
  },
  "additionalProperties": false
}`)
}

func (t *LsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lsToolArgs](args, "ls")
	if bad != nil {
		return *bad, nil
	}
	full, err := resolveWithin(t.Root, a.Path)
	if err != nil {
		return errorResult("ls: " + err.Error()), nil
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("ls: %q does not exist", a.Path)), nil
		}
		return errorResult(fmt.Sprintf("ls: cannot stat %q: %v", a.Path, err)), nil
	}
	if !info.IsDir() {
		return errorResult(fmt.Sprintf("ls: %q is not a directory", a.Path)), nil
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return errorResult(fmt.Sprintf("ls: cannot read %q: %v", a.Path, err)), nil
	}

	var dirs, files []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name()+"/")
		} else {
			files = append(files, e.Name())
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	lines := append(dirs, files...)

	label := a.Path
	if label == "" {
		label = "."
	}
	msg := fmt.Sprintf("%s (%d dir(s), %d file(s))", label, len(dirs), len(files))
	if len(lines) > 0 {
		msg += "\n" + strings.Join(lines, "\n")
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"dirs": len(dirs), "files": len(files)},
	}, nil
}
