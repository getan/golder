package judge

// Edits whose effect lands OUTSIDE the edit.
//
// This was the exception list of a routine-edit fast path: ordinary
// in-workspace patches skipped the reviewer, and these two classes did not.
// The fast path is gone — apply_patch is graded like every other call now, and
// the decision model's latency is what made that affordable — but the
// knowledge behind those exceptions is not about the exemption, it is about the
// edits, so it stays and travels to the reviewer as a fact:
//
//   - `.git/hooks/*` runs on the next git command, and git commands are the
//     ones the read-only fast path waves through all day (`git status`, `git
//     log`, `git diff`). Writing a hook plants execution the tool loop never
//     sees again; `.git/config` is the same door (core.hooksPath,
//     core.sshCommand, credential helpers). Verified rather than assumed: a
//     hook written into a scratch repository executed on the next `git commit`.
//   - AGENTS.md is read into the system prompt of every later session
//     (runtime.BuildSystemPrompt injects the working directory's copy), so a
//     patch to it rewrites the instructions the agent will start from — a
//     persistent prompt-injection channel that needs no execution at all.
//
// It is reported, not refused. A person asking for a pre-commit hook is asking
// for something legitimate, and the old code agreed: these paths kept their
// review, they were never denied. A hard deny here would take a reasonable
// request off the table; what the reviewer needs is the fact, so its verdict is
// about what the edit will do rather than about the file name alone.

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/getan/golder/internal/patch"
)

// agentsInstructionFile mirrors runtime's injected instruction file. The name is
// duplicated rather than imported because the import would be a cycle: runtime
// imports the tool layer, which imports judge.
const agentsInstructionFile = "AGENTS.md"

// OutlivingPaths returns the paths in this call whose effect outlives the call,
// or nil when there are none. It is exported for the reviewer's state: the
// decision model grades every patch now, and this is the one fact about a patch
// that its file contents do not reveal — a hook body looks like any script
// until you know when it runs.
func OutlivingPaths(tool string, args json.RawMessage) []string {
	if !strings.EqualFold(strings.TrimSpace(tool), "apply_patch") {
		return nil
	}
	text, ok := patchText(args)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range patch.Paths(text) {
		if protectedEditPath(p) {
			out = append(out, p)
		}
	}
	return out
}

// protectedEditPath reports whether a patch path names something whose effect
// outlives the edit: anything under .git, and the instruction file.
//
// The comparison folds case deliberately. macOS resolves paths
// case-insensitively, so `.GIT/hooks/x` and `agents.md` reach the same files the
// canonical spellings do, and a guard that matched only those would be one
// spelling away from useless. A false positive costs a review, which is the safe
// direction; an unreadable path keeps the review for the same reason.
func protectedEditPath(raw string) bool {
	p := strings.TrimSpace(raw)
	if p == "" {
		return true
	}
	// Patch paths are slash-separated by grammar; a backslash can only come from
	// a caller spelling a Windows path, so normalize it to stay segment-based.
	cleaned := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	for _, seg := range strings.Split(cleaned, "/") {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return strings.EqualFold(path.Base(cleaned), agentsInstructionFile)
}
