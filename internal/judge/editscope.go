package judge

// The routine-edit fast path: a patch that only touches ordinary files inside
// the workspace does not need a risk review.
//
// Why it exists. apply_patch is graded by the reviewer on every call — measured
// over ten real sessions, 467 calls at a 2.16s median, 18.5 minutes of review in
// ten sessions — and the review has almost nothing to decide: the tool already
// enforces the workspace boundary for every patch in the same hard, unwaivable
// way (agenttool.resolveWithin, which offers no grant and no prompt), so an
// in-workspace patch is the routine case by construction. The remaining question
// a reviewer could answer — "is this edit risky?" — is about the edit's content,
// which is exactly what the review's cheap tiers (confirm, and therefore auto
// mode's silent pass) do not act on anyway.
//
// Why two exceptions, and not none. `.git/**` and AGENTS.md are not "riskier
// edits"; they are edits whose effect lands OUTSIDE the edit:
//
//   - `.git/hooks/*` runs on the next git command, and git commands are the ones
//     the read-only fast path waves through all day (`git status`, `git log`,
//     `git diff`). Writing a hook plants execution the tool loop never sees
//     again; `.git/config` is the same door (core.hooksPath, core.sshCommand,
//     credential helpers). Verified rather than assumed: a hook written into a
//     scratch repository executed on the next `git commit`.
//   - AGENTS.md is read into the system prompt of every later session
//     (runtime.BuildSystemPrompt injects the working directory's copy), so a
//     patch to it rewrites the instructions the agent will start from — a
//     persistent prompt-injection channel that needs no execution at all.
//
// Both are the "the effect outlives the call" class the reviewer exists for;
// everything else — a source file, a test, a doc — is fast-pathed, which is
// nearly every patch.
//
// A patch naming anything outside the workspace keeps its review too: the one
// extra root the tool admits is the skills directory, and a skill's description
// is likewise injected into later prompts. The exemption can therefore never
// reach further than the tool already allows, and the static floor still runs
// first (a credential path in a patch is denied before this is consulted).

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/patch"
	"github.com/getan/golder/internal/permissions"
)

// agentsInstructionFile mirrors runtime's injected instruction file. The name is
// duplicated rather than imported because judge is a leaf package (runtime
// imports the tool layer, which imports judge).
const agentsInstructionFile = "AGENTS.md"

// applyPatchFastPath returns the verdict for a patch that only touches ordinary
// files inside the workspace, and whether it applies.
func applyPatchFastPath(tool string, args json.RawMessage, workspaceRoot string) (Verdict, bool) {
	if !fastPathEnabled() {
		return Verdict{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(tool), "apply_patch") {
		return Verdict{}, false
	}
	// Without the boundary there is no way to tell an in-workspace patch from one
	// reaching the skills directory, so the reviewer keeps the call.
	if strings.TrimSpace(workspaceRoot) == "" {
		return Verdict{}, false
	}
	text, ok := patchText(args)
	if !ok {
		return Verdict{}, false
	}
	paths := patch.Paths(text)
	if len(paths) == 0 {
		return Verdict{}, false
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Verdict{}, false
	}
	for _, p := range paths {
		if protectedEditPath(p) {
			return Verdict{}, false
		}
		resolved, err := permissions.AbsAgainst(root, p)
		if err != nil || !permissions.Inside(root, resolved) {
			return Verdict{}, false
		}
	}
	return Verdict{
		Level:   Allow,
		Source:  "workspace-edit",
		Reasons: []string{"in-workspace patch, outside the protected paths"},
	}, true
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
