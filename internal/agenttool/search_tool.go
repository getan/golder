// This file implements the search tools (US-019): grep (search file contents by
// regexp with optional glob filtering and context lines), find (locate files by
// name glob), and ls (list a directory, distinguishing files from directories).
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
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
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

// resolveReadable resolves p the way the READ-ONLY tools do: against the
// workspace root (or an extra root), and, when that fails, against a read grant
// the user gave for this session.
//
// This is the workspace boundary the tools enforce, kept in one place. The
// boundary is consent, not risk: reading outside the directory the user opened
// asks them first (the judge ReadScope gate), and the answer lands in
// permissions.ReadRoots (whole directory, this session) or arrives as a
// one-call grant in ctx (just this path). Both are honoured here, so a granted
// path reaches the same code a workspace path does.
//
// The write path (apply_patch) deliberately does NOT call this: a read grant
// says nothing about writing, and a write outside the workspace still needs the
// sandbox's writable-root grant.
func resolveReadable(ctx context.Context, roots []string, p string) (string, error) {
	if len(roots) == 0 {
		roots = []string{""}
	}
	full, err := resolveWithinAny(roots, p)
	if err == nil {
		return full, nil
	}
	target, terr := permissions.AbsAgainst(roots[0], p)
	if terr != nil {
		return "", err
	}
	// Credential material stays refused even for a granted directory: the
	// static floor grades the raw argument, and this catches a spelling that
	// reaches one of those locations after resolution.
	if reason, bad := judge.CredentialPathReason(target); bad {
		return "", fmt.Errorf("%s is not readable here", reason)
	}
	// A one-call grant describes the path the user was asked about. Compare
	// cleaned absolute spellings so the grant and the argument agree.
	if g := strings.TrimSpace(agentcore.ReadGrantFromContext(ctx)); g != "" && permissions.Inside(g, target) {
		return target, nil
	}
	// A session grant is a directory in the read-root registry.
	if permissions.ReadableAt(target) {
		return target, nil
	}
	return "", err
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
				return rgResult{}, rgErrorReason(errBuf.String())
			}
		}
		return rgResult{}, fmt.Sprintf("rg failed: %v", err)
	}
	return rgResult{lines: splitRGOutput(out), truncated: out.dropped}, ""
}

// rgErrorReason renders ripgrep's stderr as one actionable line. Two shapes
// arrive on exit 2. A bad pattern is a block — the header line, the pattern, a
// caret line, then "error: <reason>" — where the header only says parsing
// failed; everything else (an unknown --type, an unreadable path) is a single
// "rg: <reason>" line. So the reason line wins when there is one, and a parse
// failure is labelled an invalid pattern, keeping the message as short as the
// local pre-check's used to be.
//
// The header itself is version-dependent: ripgrep 13 (Debian 12, Ubuntu 22.04)
// prints "regex parse error:", while 14+ prefixes it with the program name
// ("rg: regex parse error:"). Matching the phrase rather than the prefixed
// line is what makes the label appear on both — keying off the "rg: " line kept
// the message bare "unrecognized escape sequence" on the older builds, which is
// how this surfaced: a Debian 12 container failed a test that passed against
// ripgrep 15 on the development machine.
func rgErrorReason(stderr string) string {
	reason := ""
	for _, ln := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "error: "); ok {
			reason = rest
			break
		}
	}
	if reason != "" {
		if strings.Contains(stderr, "regex parse error") {
			return "invalid pattern: " + reason
		}
		return reason
	}
	for _, ln := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "rg: "); ok {
			return rest
		}
	}
	return firstNonEmptyLine(stderr)
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

// rgScope picks the working directory and path argument for a search after the
// start path has been resolved, which may be a user-granted read root outside
// the workspace. A start inside the workspace keeps the historical shape: run
// from the workspace and name the relative path, so results stay workspace-
// relative. A granted start outside it is searched directly, so results are
// relative to what was actually searched instead of a chain of "../..".
func rgScope(root, start string) (dir string, pathArg []string) {
	if !permissions.Inside(root, start) {
		if info, err := os.Stat(start); err == nil && info.IsDir() {
			return start, nil
		}
		return filepath.Dir(start), rgSearchPath(filepath.Base(start))
	}
	rel, err := filepath.Rel(root, start)
	if err != nil {
		return root, nil
	}
	return root, rgSearchPath(rel)
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
	// Context shows this many lines around each match (rg -C), so reading the
	// code around a hit does not need a shell. 0 shows matches only.
	Context int `json:"context,omitempty"`
	// IgnoreCase matches case-insensitively (rg -i).
	IgnoreCase bool `json:"ignore_case,omitempty"`
	// Word matches whole words only (rg -w).
	Word bool `json:"word,omitempty"`
	// Literal treats the pattern as a literal string (rg -F), so a search for
	// code that contains regexp metacharacters needs no escaping.
	Literal bool `json:"literal,omitempty"`
	// Invert reports the lines that do NOT match (rg -v).
	Invert bool `json:"invert,omitempty"`
	// FilesOnly lists the names of the files that match instead of the
	// matching lines (rg -l).
	FilesOnly bool `json:"files_only,omitempty"`
	// Type filters files by ripgrep's file type (rg -t, e.g. "go", "rust").
	Type string `json:"type,omitempty"`
	// Hidden also searches hidden files and directories (rg --hidden). The tree
	// walk then still skips .git, whose object blobs and refs are noise in a
	// content search; an explicit path into .git is not subject to it.
	Hidden bool `json:"hidden,omitempty"`
	// NoIgnore also searches files excluded by .gitignore (rg --no-ignore).
	NoIgnore bool `json:"no_ignore,omitempty"`
}

// grepMaxContext bounds the context argument. Reading a whole file is what the
// read tool is for; context is for seeing a match in place, and a generous cap
// keeps every ordinary "show me around this hit" out of a shell pipeline.
const grepMaxContext = 50

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Search file contents by regular expression under the workspace " +
		"(ripgrep). Filters files by glob or type; also takes context lines " +
		"around each match, ignore_case, word, literal, invert, files_only, " +
		"hidden and no_ignore. Skips .gitignore'd, hidden, and binary files by " +
		"default. Independent searches run faster batched in one message (one " +
		"call per pattern or directory)."
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
    "glob":    {"type": "string", "description": "Filter files by base-name glob, e.g. *.go."},
    "context": {"type": "integer", "description": "Lines of context to show around each match (rg -C). 0 (the default) shows matches only.", "minimum": 0, "maximum": 50},
    "ignore_case": {"type": "boolean", "description": "Match case-insensitively (rg -i)."},
    "word":        {"type": "boolean", "description": "Match whole words only (rg -w)."},
    "literal":     {"type": "boolean", "description": "Treat the pattern as a literal string (rg -F), so metacharacters need no escaping."},
    "invert":      {"type": "boolean", "description": "Report the lines that do NOT match (rg -v)."},
    "files_only":  {"type": "boolean", "description": "List the names of the files that match instead of the matching lines (rg -l)."},
    "type":        {"type": "string", "description": "Filter files by ripgrep file type (rg -t), e.g. go, rust, py."},
    "hidden":      {"type": "boolean", "description": "Also search hidden files and directories (rg --hidden). The tree search still skips .git."},
    "no_ignore":   {"type": "boolean", "description": "Also search files excluded by .gitignore (rg --no-ignore)."}
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
	if a.Context < 0 || a.Context > grepMaxContext {
		return errorResult(fmt.Sprintf("grep: context must be between 0 and %d", grepMaxContext)), nil
	}
	root, err := resolveWithin(t.Root, "")
	if err != nil {
		return errorResult("grep: " + err.Error()), nil
	}
	start := root
	if a.Path != "" {
		if start, err = resolveReadable(ctx, []string{t.Root}, a.Path); err != nil {
			return errorResult("grep: " + err.Error()), nil
		}
	}
	// The search may start outside the workspace when the user granted that
	// directory for reading; rgScope keeps workspace results workspace-relative
	// and roots an outside search at the granted directory itself.
	dir, pathArg := rgScope(root, start)

	// Pattern validation is ripgrep's alone. A local regexp.Compile pre-check
	// used to gate this, but it graded the pattern with Go's regexp while
	// ripgrep matches with the Rust regex crate, and the two dialects disagree
	// in both directions: `\Qfoo(bar)\E` compiles in Go and is rejected by rg
	// (so the search failed anyway, after the check passed), while rg's verbose
	// mode `(?x)` is rejected by Go and works in rg (so a legal search was
	// refused with "invalid pattern"). ripgrep's own verdict is the only one
	// that describes what will actually run; rgErrorReason turns its multi-line
	// parse dump back into the short message the pre-check used to produce.
	rgArgs := []string{"--line-number", "--no-heading", "--with-filename", "--regexp", a.Pattern}
	if a.IgnoreCase {
		rgArgs = append(rgArgs, "--ignore-case")
	}
	if a.Word {
		rgArgs = append(rgArgs, "--word-regexp")
	}
	if a.Literal {
		rgArgs = append(rgArgs, "--fixed-strings")
	}
	if a.Invert {
		rgArgs = append(rgArgs, "--invert-match")
	}
	if a.FilesOnly {
		// -l prints the paths alone, so the line-oriented flags above and the
		// context machinery below do not apply to it.
		rgArgs = append(rgArgs, "--files-with-matches")
	}
	if a.Type != "" {
		rgArgs = append(rgArgs, "--type="+a.Type)
	}
	if a.Hidden {
		// --hidden reaches .git, whose object blobs and refs are noise in a
		// content search, so the exclusion is appended LAST to override any
		// user glob (ripgrep applies globs in order, later ones win). Hygiene
		// rather than a boundary: an explicitly given path into .git is not
		// subject to ignore rules, and the read tool can already open those
		// files.
		rgArgs = append(rgArgs, "--hidden")
	}
	if a.NoIgnore {
		rgArgs = append(rgArgs, "--no-ignore")
	}
	if a.Context > 0 {
		// --null NUL-terminates the path, which is what lets grepResultLines
		// tell a context line from a match (see its comment).
		rgArgs = append(rgArgs, "--null", fmt.Sprintf("--context=%d", a.Context))
	}
	if a.Glob != "" {
		rgArgs = append(rgArgs, "--glob="+a.Glob)
	}
	if a.Hidden {
		rgArgs = append(rgArgs, "--glob=!.git")
	}
	rgArgs = append(rgArgs, pathArg...)

	res, rgMsg := runRG(ctx, dir, rgArgs)
	if rgMsg != "" {
		return errorResult("grep: " + rgMsg), nil
	}

	if a.FilesOnly {
		// -l output has no line numbers, so it takes its own shape: the cap
		// counts files, and there is no context to parse.
		files := res.lines
		truncated := res.truncated
		if len(files) > searchMaxResults {
			files = files[:searchMaxResults]
			truncated = true
		}
		msg := fmt.Sprintf("%d file(s) matching %q", len(files), a.Pattern)
		if len(files) > 0 {
			msg += "\n" + strings.Join(files, "\n")
		}
		if truncated {
			msg += "\n" + truncationMarker("files", len(files))
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
			Details: map[string]any{"files": len(files)},
		}, nil
	}

	kept, matches, truncated := capGrepLines(grepResultLines(res.lines, a.Context), searchMaxResults)
	shown := make([]string, len(kept))
	for i, ln := range kept {
		shown[i] = ln.text
	}
	msg := fmt.Sprintf("%d match(es) for %q", matches, a.Pattern)
	if a.Context > 0 {
		msg += fmt.Sprintf(" (with %d line(s) of context)", a.Context)
	}
	if len(shown) > 0 {
		msg += "\n" + strings.Join(shown, "\n")
	}
	if truncated || res.truncated {
		msg += "\n" + truncationMarker("matches", matches)
	}
	details := map[string]any{"matches": matches}
	if a.Context > 0 {
		details["context"] = a.Context
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: details,
	}, nil
}

// grepResultLine is one line of a grep result: the text the model sees, and
// whether it is a match (as opposed to a context line or a group separator).
type grepResultLine struct {
	text  string
	match bool
}

// grepResultLines shapes ripgrep's stdout for the tool. Without context every
// line is a match and the text passes through unchanged. With context the
// capture is ripgrep's --null form, because a plain path followed by ':' or
// '-' is ambiguous: a file named `a-b.go` searched at line 2 prints a match as
// `a-b.go:2:text` and a context line as `a-b.go-1-context`, and nothing in the
// line says where the path ends — `x-1-2-y` is a context line in `x-1` or one
// in `x`. The NUL ends the path unambiguously, and the separator that follows
// the line number says which kind of line it is. Both are then rewritten into
// ripgrep's plain form, so the model sees the shape a shell would print (and
// the two separators keep meaning match and context).
func grepResultLines(lines []string, context int) []grepResultLine {
	out := make([]grepResultLine, 0, len(lines))
	for _, ln := range lines {
		if context == 0 {
			out = append(out, grepResultLine{text: ln, match: true})
			continue
		}
		nul := strings.IndexByte(ln, 0)
		if nul < 0 {
			// The `--` separator between match groups, printed as-is.
			out = append(out, grepResultLine{text: ln})
			continue
		}
		rest := ln[nul+1:]
		sep := rgLineSeparator(rest)
		out = append(out, grepResultLine{text: ln[:nul] + string(sep) + rest, match: sep == ':'})
	}
	return out
}

// rgLineSeparator returns the separator ripgrep printed between a line number
// and its text: ':' for a match, '-' for a context line. rest is what follows
// the --null path terminator, so it starts with the line number.
func rgLineSeparator(rest string) byte {
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return rest[i]
		}
	}
	return ':'
}

// capGrepLines keeps matches up to maxMatches with the context lines that
// belong to them; the trailing context of the last kept match is kept too,
// since the cut happens at the next match. matches is the number of match
// lines kept (what the result message and Details carry), so a context search
// reports matches rather than printed lines, and truncated says a further
// match was dropped.
func capGrepLines(lines []grepResultLine, maxMatches int) (kept []grepResultLine, matches int, truncated bool) {
	for _, ln := range lines {
		if ln.match {
			if matches >= maxMatches {
				// A group separator printed before the dropped match is no
				// longer separating anything.
				for len(kept) > 0 && kept[len(kept)-1].text == "--" {
					kept = kept[:len(kept)-1]
				}
				return kept, matches, true
			}
			matches++
		}
		kept = append(kept, ln)
	}
	return kept, matches, false
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
		"Skips .gitignore'd and hidden files. Batch independent lookups in the " +
		"same message — they run in parallel."
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
		if start, err = resolveReadable(ctx, []string{t.Root}, a.Path); err != nil {
			return errorResult("find: " + err.Error()), nil
		}
	}
	dir, pathArg := rgScope(root, start)

	rgArgs := []string{"--files", "--glob=" + a.Glob}
	rgArgs = append(rgArgs, pathArg...)

	res, rgMsg := runRG(ctx, dir, rgArgs)
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
	return "List a directory's entries, marking directories with a trailing slash. " +
		"Batch several directories in one message — they run in parallel."
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
	full, err := resolveReadable(ctx, []string{t.Root}, a.Path)
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
