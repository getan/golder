package cli

// Rebuilding a resumed session's system prompt.
//
// The prompt is a mix of two very different things, and the session file used
// to freeze both: our own description of the current binary (tool guide,
// environment block with the working directory and date, whether ripgrep is
// installed, AGENTS.md, the installed skills) and the user's own words
// (--system-prompt, --append-system-prompt). Freezing the first half is what
// made a resumed session advise the model about flags that had since shipped,
// carry yesterday's date, and ignore edits to AGENTS.md.
//
// So the split is now explicit: our half is recomputed from the launch, the
// user's half is carried over from the session header. The recorded inputs
// (SessionHeader.BaseInstruction / AppendInstructions) are what makes the
// second half possible.

import (
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

// ResumeSystemPrompt returns the system prompt a resumed session should run
// under: the current binary's guide, environment block, AGENTS.md and skills,
// with the user-authored inputs preserved — the session's recorded
// --system-prompt base and --append-system-prompt blocks, plus this launch's
// flags, which win where given (passing a flag means the user is telling us
// now).
//
// A session written before the header recorded those inputs has none, so its
// stored prompt is replaced outright by a freshly built one. That is the point
// of the change — but it does mean a custom base in such a session (if one ever
// existed; every session on this machine is the default) is not recoverable,
// because the input was never kept.
//
// A rebuild failure (an AGENTS.md that exists but cannot be read) falls back to
// the stored prompt: a resume must not break over a file it did not need.
func ResumeSystemPrompt(h session.SessionHeader, launch runtime.PromptInputs) string {
	merged := launch
	if merged.Base == "" {
		merged.Base = h.BaseInstruction
	}
	if len(h.AppendInstructions) > 0 {
		// The session's blocks come first, then this launch's, matching the
		// order a fresh session appends them in.
		merged.Appends = append(append([]string{}, h.AppendInstructions...), launch.Appends...)
	}
	rebuilt, err := merged.Build()
	if err != nil || rebuilt == "" {
		return h.SystemPrompt
	}
	return rebuilt
}
