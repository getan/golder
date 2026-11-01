// This file is the headless run driver: the print / stream-json run path
// (US-020) extracted from the CLI dispatch seam (#363). dispatch resolves the
// output mode and the run environment, then hands off to Run, which wires the
// session, prompt, thinking level, and provider credentials into a
// runtime.HeadlessConfig and executes one run. Output-mode parsing lives here
// because it is specific to the headless path.
package headless

import (
	"context"
	"fmt"
	"io"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// RunParams carries the resolved inputs for one headless run. Mode and Env are
// resolved by the caller (dispatch) — Mode via ParseOutputMode, Env via
// run.SetupEnv — so their distinct exit codes stay at the call site; Run owns
// the rest of the run lifecycle.
type RunParams struct {
	Mode          runtime.HeadlessMode
	Env           run.Env
	Prompt        string
	Model         string
	APIKey        string
	ThinkingLevel string
	ResumeID      string
	// MaxContext is the resolved [compaction] max_context value, applied to
	// the run's auto-compaction trigger. The zero value leaves the defaults.
	MaxContext config.MaxContext
}

// Run executes one headless run over p.Prompt, writing agent output to out and
// diagnostics to errOut, and returns a process exit code (0 = success). The run
// is backed by a session so its id appears in the first stream-json event and it
// can be resumed with --resume/--continue; a resumed session seeds its prior
// messages ahead of the new prompt.
func Run(ctx context.Context, p RunParams, out, errOut io.Writer) int {
	env := p.Env
	// Kill still-running shell sessions on exit so a long-running command handed
	// back as a bash_id is not orphaned when the one-shot run finishes.
	defer run.KillShellSessions(env.Tools)
	promptContent, err := ui.BuildUserContent(p.Prompt)
	if err != nil {
		fmt.Fprintf(errOut, "golder: %v\n", err)
		return 1
	}

	// Back the headless run with a session so its id appears in the first
	// stream-json event and the run can be resumed with --resume/--continue,
	// matching the interactive REPL and pi/Claude Code. A resumed session seeds
	// its prior messages ahead of the new prompt.
	priorMsgs, hs, err := openHeadlessSession(p.ResumeID, p.Model, env.ProviderName, env.SysPrompt, env.Prompt)
	if err != nil {
		fmt.Fprintf(errOut, "golder: %v\n", err)
		return 1
	}
	messages := append(priorMsgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: promptContent})
	agentCtx := &agentcore.AgentContext{
		SystemPrompt: hs.header.SystemPrompt,
		Messages:     messages,
		Tools:        env.Tools,
	}

	// Resolve the effective reasoning-effort level through the layered config
	// chain (default < global < project < env < --thinking-level flag).
	thinking, err := run.ResolveThinkingLevel(p.ThinkingLevel)
	if err != nil {
		fmt.Fprintf(errOut, "golder: %v\n", err)
		return 2
	}

	// Resolve the API key by provider name from the environment (never logged).
	// An explicit --api-key overrides env/config for the resolved provider.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(env.ProviderName, p.APIKey)
	runCfg := run.NewConfig(p.Model, env.ProviderName, thinking, env.Provider, creds, run.ToolRegistry(env.Tools), run.TodoReminders(env.Tools), env.Schedule, env.Budget)
	run.InstallExtensions(&runCfg, env.Extensions, env.Prompt)
	// Resolve the window from the models.dev catalog when it knows the model,
	// falling back to the REPL/TUI default. This keeps auto-compaction, the
	// context-budget tools, and the low-budget reminder live for `golder -p`
	// long runs, and sizes them to the model actually in use.
	runCfg.ContextWindow = cli.ResolveContextWindow(env.ProviderName, p.Model)
	// Apply the configured max_context lower bound on top of the resolved
	// window, so `golder -p` long runs honor the same knob as the TUI.
	runCfg.Compaction = cli.CompactionSettingsFor(p.MaxContext, runCfg.ContextWindow)
	runCfg.SessionID = hs.header.ID
	// Route auto-compaction checkpoints to the shared memory root so a rebuild can
	// recover the pre-watermark prefix (no-op when memory is disabled → empty root).
	runCfg.MemoryRoot = run.MemoryRootFromTools(env.Tools)
	// Headless has no stdin prompt, so the permission gate runs
	// non-interactively: auto mode lets the reviewer decide (Confirm runs,
	// Sandbox is isolated, Deny blocks) and ask mode fails closed. Decisions
	// go to stderr so stdout stays a clean answer stream.
	//
	// The chain comes from the one assembly point (run.Gates) with no answer
	// channels: reading outside the workspace blocks with guidance naming the
	// path and the config that would allow it (a script cannot approve, and
	// guessing on its behalf would quietly widen what data the run touches),
	// and the same chain is what a task child inherits. The trust manager is
	// nil here — headless is an explicit non-interactive invocation, so the
	// invocation itself is the authorization; its snapshot tells a
	// process-isolated child the same.
	notes := run.NewReviewNotes()
	notes.Set(func(n judge.Note) { fmt.Fprintln(errOut, judge.FormatNote(n)) })
	// Report to the multiplexer around this pane, when there is one. A
	// one-shot run reports the same lifecycle an interactive one does — the
	// conversation id, one turn, and a release on the way out — so a person who
	// runs `golder -p ...` in a pane sees the same row on their rail as for any
	// other agent. Nil outside such a pane, and nil is a working no-op.
	host := hostcompat.OpenSession(hostcompat.Options{ErrLog: errOut})
	host.Start(hs.header.ID, hostcompat.ResumeArgv(hs.header.ID))
	gates := run.NewGates(run.GatesOpts{
		Cwd:       env.Cwd,
		Perms:     env.Permissions,
		Notes:     notes,
		Sandboxed: run.SandboxGate(),
		Classifier: func() judge.Classifier {
			return run.NewReviewer(p.Model, env.ProviderName, env.Provider, creds, hs.header.ID, env.Cwd, run.Trusted(env.Cwd))
		},
		// A headless run has no answer channel of its own — a script cannot
		// approve anything — but it still reports a question as a pane waiting
		// for a person, which is exactly what the person needs to see when a
		// headless run blocks on one.
		OnPrompt:   func(question string) func() { return host.Block(hostcompat.KindApproval, question) },
		ExtraRoots: run.ReadableExtraRoots(),
	})

	// Wire hooks uniformly with every other driver (#425): resolve the trust-gated
	// hook set, install the tool-execution + Stop seams, dispatch SessionStart, and
	// install the SessionEnd/PreCompact observer. A
	// malformed hook layer is a config error (exit 2), matching thinking-level.
	source := "startup"
	if p.ResumeID != "" {
		source = "resume"
	}
	set, herr := run.ResolveHookSet(env.Cwd, run.Trusted(env.Cwd))
	if herr != nil {
		fmt.Fprintf(errOut, "golder: %v\n", herr)
		return 2
	}
	hookDeps := run.HookDeps{SessionID: hs.header.ID, ProjectDir: env.Cwd, WarnLog: errOut}
	d, onEvent := gates.InstallDriver(ctx, &runCfg, set, hookDeps, source, nil)
	// UserPromptSubmit runs before the prompt is handed to the loop: a block aborts
	// the headless run non-zero; additionalContext is injected into this run only.
	if d != nil {
		if block, reason := run.DispatchUserPromptSubmit(ctx, d, &runCfg, hookDeps, p.Prompt); block {
			fmt.Fprintf(errOut, "golder: prompt blocked by hook: %s\n", reason)
			return 1
		}
	}

	cfg := runtime.HeadlessConfig{
		Mode: p.Mode,
		Out:  out,
		Run:  runCfg,
	}
	cfg.OnEvent = onEvent
	// One turn, reported as one turn: the multiplexer sees golder start working
	// and then finish (or fail), so a person watching the rail sees the headless
	// run the same way they see an interactive one.
	host.TurnStart()
	defer func() {
		host.Close()
	}()
	runErr := runtime.RunHeadless(ctx, agentCtx, cfg)
	host.TurnEnd(runErr != nil)
	// Persist the run's messages regardless of run outcome so a partial run is
	// still resumable; a persistence failure is reported but does not mask a run
	// error.
	if perr := hs.persist(agentCtx); perr != nil {
		fmt.Fprintf(errOut, "golder: warning: could not persist session %s: %v\n", hs.header.ID, perr)
	}
	if runErr != nil {
		fmt.Fprintf(errOut, "golder: %v\n", runErr)
		return 1
	}
	return 0
}

// ParseOutputMode maps the --output-format flag onto a HeadlessMode, erroring on
// an unknown value.
func ParseOutputMode(outputFmt string) (runtime.HeadlessMode, error) {
	switch outputFmt {
	case "text", "":
		return runtime.PrintMode, nil
	case "stream-json":
		return runtime.StreamJSONMode, nil
	default:
		return 0, fmt.Errorf("unknown --output-format %q (want text|stream-json)", outputFmt)
	}
}
