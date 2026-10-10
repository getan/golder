package ui

// Interrupt and quit notices, shared by both front-ends.
//
// Ctrl+C used to speak differently in every branch and in every front-end:
// the TUI printed "(interrupting the current run…)" on the press and then
// "Run ended: context canceled" or "error: aborted" when the run settled,
// while the REPL printed "^C interrupted" or "error: aborted" — and two
// branches (discarding a draft, copying a selection) printed nothing at all.
// The same keystroke read as five different things, which is what made the
// behavior feel inconsistent even when it was working. These constants are the
// single source of that wording, so the press and its settlement read the same
// everywhere.
const (
	// InterruptingNotice acknowledges the keypress while the run unwinds. The
	// cancellation is a request, so the notice must not promise the run has
	// already stopped.
	InterruptingNotice = "(interrupting…)"

	// InterruptingQueuedNotice is the same acknowledgement when input is queued:
	// the interrupt doubles as "send it now", so the notice says what comes next.
	InterruptingQueuedNotice = "(interrupting — queued input will be sent next)"

	// ShellInterruptingNotice acknowledges a press aimed at a `!` passthrough
	// command, which is a process of its own rather than an agent run.
	ShellInterruptingNotice = "(interrupting the shell command…)"

	// InterruptNotice is the settlement line: the run has actually ended because
	// it was interrupted. It is the one line both front-ends print for that, so
	// an interrupted turn is recognizable without reading the surrounding text.
	InterruptNotice = "Interrupted."

	// DraftDiscardedNotice reports the shell-like first stage of Ctrl+C: the
	// composer was thrown away, and nothing else happened.
	DraftDiscardedNotice = "(draft discarded)"

	// SelectionCopiedNotice reports the Ctrl+C copy branch, so the press is
	// acknowledged instead of silently doing nothing visible.
	SelectionCopiedNotice = "(selection copied)"

	// QuitArmedNotice reports the armed idle quit, which needs one more press.
	QuitArmedNotice = "Press Ctrl+C again to quit."
)
