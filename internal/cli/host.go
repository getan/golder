// This file defines the Host and Editor contracts that let the /goal, /btw,
// /status and REPL subpackages read and mutate a session's live state without
// importing the concrete replDeps aggregate that assembles it (see doc.go). The
// aggregate implements Host by exposing accessor and mutator methods over its
// fields; the line editor implements Editor.
package cli

import (
	"bufio"
	"errors"
	"io"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/plugin"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
	"github.com/getan/golder/internal/trust"
)

// ErrLineInterrupted is returned by an Editor.ReadLine when the user hits an
// idle Ctrl+C at the prompt (rather than during a run). Control commands that
// run their own follow-up loop (e.g. /btw) check for it with errors.Is to leave
// the loop cleanly. It lives here, beside the Editor contract, so a subpackage
// can recognize the interrupt without importing the concrete line editor.
var ErrLineInterrupted = errors.New("line input interrupted")

// Host is the seam through which a control command (/goal, /btw, /status) and
// the REPL loop access the collaborators and mutable state of a session. It is
// satisfied by the replDeps aggregate assembled once per session.
//
// Getters return the shared collaborators (never copies, except where noted).
// The setters cover the fields a command may advance mid-session: the active
// session leaf, the persisted-message count, and the last /btw side thread.
type Host interface {
	// Session collaborators.
	Store() *session.Store
	Header() session.SessionHeader
	AgentCtx() *agentcore.AgentContext
	Live() *LiveConfig
	Registry() *agenttool.ToolRegistry
	Reminders() *runtime.ReminderRegistry
	Slash() *runtime.SlashRegistry
	Creds() *provider.CredentialStore
	Notifier() *plugin.EventNotifier
	// NotifierHandle returns the plugin event-delivery callback for this
	// session's runs, or nil when no plugin subscribed.
	NotifierHandle() func(agentcore.AgentEvent)
	Trust() *trust.Manager
	// Permissions returns the live approval mode shared by the session's gates
	// and tools. /goal and /btw build their own runs and need it to enforce the
	// same mode as the main conversation.
	Permissions() *permissions.State
	// ReviewNotes returns the announcement sink for permission decisions, so a
	// side run's gate publishes decisions to the same transcript surface as the
	// main run.
	ReviewNotes() *run.ReviewNotes
	Goal() *agenttool.GoalState
	Telemetry() *TelemetryHolder

	// Dispatcher returns the session's hook dispatcher, or nil when no hooks are
	// configured (FR-18). Side-runs (/goal, /btw) install the per-turn seams onto
	// their own cfg via run.InstallSeams(cfg, host.Dispatcher(), host.HookDeps()).
	Dispatcher() *hooks.Dispatcher
	// HookDeps carries the session id / project dir stamped onto every HookInput.
	HookDeps() run.HookDeps
	// PromptNote returns the session's hook for "a prompt is about to be put to
	// the person", or nil when the session reports to no terminal multiplexer.
	// Side-runs (/goal, /btw) install it on their own gates so a question asked
	// from one of them also shows up as a pane waiting for a human, exactly as
	// one asked from the main conversation does. See internal/hostcompat.
	PromptNote() func(question string) func()

	// Cwd is the directory golder was launched in; it does not change during a
	// session and gates side-effect tools.
	Cwd() string
	// Input is the shared buffered stdin reader used by both the main loop and
	// the tool-call confirmation prompt.
	Input() *bufio.Reader
	// ConfirmMu serializes tool-call confirmation prompts across concurrent
	// side-effect tool calls.
	ConfirmMu() *sync.Mutex

	// Session-tree cursor and persistence bookkeeping.
	CurLeaf() string
	SetCurLeaf(id string)
	Persisted() int
	SetPersisted(n int)

	// Last /btw side thread from this process and its background base index.
	LastBtw() *agentcore.AgentContext
	SetLastBtw(ctx *agentcore.AgentContext)
	LastBtwBase() int
	SetLastBtwBase(n int)
}

// HostGates assembles a session's gate chain from the Host contract, so a
// command that drives its own run (/goal, /btw) inherits exactly the gates of
// the run that invoked it rather than re-listing them. It is the same
// run.Gates the main run installs; only the output writer differs, which is why
// the caller passes the writer its run prints to.
func HostGates(host Host, out io.Writer) *run.Gates {
	return run.NewGates(run.GatesOpts{
		Trust:      host.Trust(),
		Cwd:        host.Cwd(),
		Perms:      host.Permissions(),
		Notes:      host.ReviewNotes(),
		Sandboxed:  run.SandboxGate(),
		Classifier: func() judge.Classifier { return HostReviewer(host) },
		In:         host.Input(),
		Out:        out,
		Mu:         host.ConfirmMu(),
		// A question asked from a side run is a pane waiting for a person just
		// as much as one asked from the main conversation.
		OnPrompt:   host.PromptNote(),
		ExtraRoots: run.ReadableExtraRoots(),
	})
}

// HostReviewer builds the reviewer for the session's active model, rebuilt per
// chain so a /model switch changes the reviewer with the conversation.
func HostReviewer(host Host) judge.Classifier {
	live := host.Live()
	return run.NewReviewer(live.Model, live.ProviderName, live.Provider, host.Creds(),
		host.Header().ID, host.Cwd(), host.Trust() != nil && host.Trust().IsTrusted(host.Cwd()))
}

// Editor is the line-input contract a control command uses to read a follow-up
// line from the user (e.g. the /btw follow-up loop). It is satisfied by the
// REPL's line editor.
type Editor interface {
	ReadLine(prompt string) (string, error)
}
