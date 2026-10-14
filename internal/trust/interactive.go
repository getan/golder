// This file holds the interactive pieces of project trust (US-018, #134). The
// trust store itself lives alongside in manager.go; here are the REPL-facing
// parts:
//
//   - the first-run trust dialog (EnsureTrustPrompt), shown when the cwd has no
//     saved decision;
//   - the /trust command (RegisterCommand), which saves or reports the
//     current project's decision;
//   - the BeforeToolCall hook (BeforeToolCall) that asks before side-effect
//     tools (bash/write/edit) run in an untrusted directory.
//
// All prompts share the REPL's single *bufio.Reader so input typed ahead is
// never split between the main loop and a confirmation. Headless mode is
// unaffected: trust is a REPL safety feature and headless is an explicit,
// non-interactive invocation.
package trust

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/patch"
	"github.com/getan/golder/internal/runtime"
)

// errPromptInterrupt marks a prompt abandoned by SIGINT. It is internal: the
// prompt translates it to its safe default (deny / def), never surfaces it.
var errPromptInterrupt = errors.New("prompt interrupted")

// SideEffectTools are the built-in tools with filesystem or process side
// effects that trust always gates. apply_patch is the single write-path tool
// (it replaced write/edit), so gating it gates every file mutation the model
// can make.
var SideEffectTools = map[string]bool{
	"bash":        true,
	"apply_patch": true,
}

// ReadTools are the data-ingestion tools (local file reads and network reads)
// gated in untrusted directories only when Manager.StrictReads is set. Pure
// in-memory tools (todo, schedule_*, goal_*, memory_*) are never gated.
var ReadTools = map[string]bool{
	"read":       true,
	"view_image": true,
	"grep":       true,
	"find":       true,
	"ls":         true,
	"webfetch":   true,
	"websearch":  true,
}

// LocalReadTools are the read tools an "allow reads here" answer covers: the
// ones that read the folder's own files. Network reads (webfetch, websearch)
// are deliberately outside it - the folder key says nothing about egress, so
// covering them would open the session's network path on a keystroke meant for
// files. They stay gated and keep asking each time.
var LocalReadTools = map[string]bool{
	"read":       true,
	"view_image": true,
	"grep":       true,
	"find":       true,
	"ls":         true,
}

// GatesTool reports whether the trust gate applies to a tool call in an
// untrusted directory cwd: always for side-effect tools, and for read tools
// under StrictReads unless cwd already carries a session reads grant (the
// gate's middle rung, which covers LocalReadTools only). A nil manager gates
// nothing.
func (m *Manager) GatesTool(name, cwd string) bool {
	if SideEffectTools[name] {
		return true
	}
	if m == nil || !m.StrictReads || !ReadTools[name] {
		return false
	}
	return !(LocalReadTools[name] && m.ReadsTrusted(cwd))
}

// EstablishTrust decides how the launch directory's trust is established before
// the REPL runs. When approve is true (mirrors pi's --approve/-a), it grants
// session trust up front so the first-launch prompt is skipped and side-effect
// tools run without per-call confirmation. Otherwise it defers to
// EnsureTrustPrompt, which asks only on the first launch in an undecided
// directory. mgr==nil disables trust entirely, so approve is a no-op.
func EstablishTrust(out io.Writer, in *bufio.Reader, mgr *Manager, cwd string, approve bool) {
	if approve {
		if mgr != nil {
			mgr.SetSessionTrust(cwd)
		}
		return
	}
	EnsureTrustPrompt(out, in, mgr, cwd)
}

// EnsureTrustPrompt runs the first-run trust dialog when cwd has no saved
// decision (NearestTrustDecision reports Found=false). When a decision already
// exists (trusted/untrusted/null) it is a no-op: the user already answered, so
// golder does not re-ask on every launch. mgr==nil disables trust entirely.
func EnsureTrustPrompt(out io.Writer, in *bufio.Reader, mgr *Manager, cwd string) {
	if mgr == nil {
		return
	}
	if res := mgr.NearestTrustDecision(cwd); res.Found {
		return
	}
	fmt.Fprintf(out, "\nFirst time in this directory: %s\n", cwd)
	fmt.Fprintln(out, "golder runs side-effect tools (bash, write, edit) and reads files (read, grep, webfetch) here.")
	fmt.Fprintln(out, "Choose a trust level:")
	fmt.Fprintln(out, "  1) Trust     - remember as trusted (tools run without asking)")
	fmt.Fprintln(out, "  2) Just once - trust only for this session (default)")
	fmt.Fprintln(out, "  3) Reject    - do not trust (tools ask each time)")

	// Default to "just once" (2): it keeps the REPL usable without persisting
	// a trust grant the user did not explicitly confirm.
	choice := readMenuChoice(out, in, "Enter choice [1-3]: ", 3, 2)
	switch choice {
	case 1:
		target := chooseScope(out, in, cwd)
		if err := mgr.SetDecision(target, Trusted); err != nil {
			fmt.Fprintf(out, "golder: could not save trust decision: %v\n", err)
			return
		}
		fmt.Fprintf(out, "Trusted %s (saved to %s).\n", cwd, target)
	case 3:
		target := chooseScope(out, in, cwd)
		if err := mgr.SetDecision(target, Untrusted); err != nil {
			fmt.Fprintf(out, "golder: could not save trust decision: %v\n", err)
			return
		}
		fmt.Fprintf(out, "Marked %s untrusted (saved to %s). Tools will ask before they run.\n", cwd, target)
	default: // 2 = just once
		mgr.SetSessionTrust(cwd)
		fmt.Fprintf(out, "Trusted %s for this session only (not saved).\n", cwd)
	}
}

// chooseScope asks whether to save the decision for the current directory or
// its parent, returning the chosen path. On empty/EOF it defaults to the
// current directory.
func chooseScope(out io.Writer, in *bufio.Reader, cwd string) string {
	parent := filepath.Dir(filepath.Clean(cwd))
	if parent == filepath.Clean(cwd) {
		// cwd is the filesystem root: there is no parent, so do not offer one.
		return cwd
	}
	fmt.Fprintln(out, "Save for:")
	fmt.Fprintf(out, "  1) This directory (%s)\n", cwd)
	fmt.Fprintf(out, "  2) Parent directory (%s)\n", parent)
	if readMenuChoice(out, in, "Enter [1-2]: ", 2, 1) == 2 {
		return parent
	}
	return cwd
}

// readMenuChoice prompts and reads a 1..max integer, re-prompting on invalid
// input. An empty line or EOF returns def so the dialog never deadlocks on
// missing input. A SIGINT also returns def: the startup dialog runs before
// any run context exists, so there is nothing to cancel — falling back to
// the default (session-only, never persisted) instead of trapping the user.
func readMenuChoice(out io.Writer, in *bufio.Reader, prompt string, max, def int) int {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	for {
		fmt.Fprint(out, prompt)
		line, err := readLineOrSignal(in, sigCh)
		if err != nil && line == "" {
			return def
		}
		s := strings.TrimSpace(line)
		if s == "" {
			return def
		}
		if n, parseErr := strconv.Atoi(s); parseErr == nil && n >= 1 && n <= max {
			return n
		}
		fmt.Fprintf(out, "  (enter a number 1-%d)\n", max)
		if err != nil {
			return def
		}
	}
}

// readLineOrSignal reads one line, aborting to ("", errInterrupt) on SIGINT
// so a startup prompt never traps Ctrl+C. The orphaned read may deliver
// late input to the next prompt on the shared reader; that matches the
// existing typed-ahead contract (input is never split between prompts).
func readLineOrSignal(in *bufio.Reader, sigCh <-chan os.Signal) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := in.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-sigCh:
		return "", errPromptInterrupt
	}
}

// RegisterCommand installs the /trust action command, which saves or
// reports the current project's trust decision. It is an instance built-in
// (AddBuiltin) because its closure captures the trust manager and cwd - state
// out of reach of an init()-time global registration. mgr==nil is a no-op: the
// command is not installed, so /trust reports unknown when trust is disabled.
func RegisterCommand(reg *runtime.SlashRegistry, mgr *Manager, cwd string) {
	if mgr == nil {
		return
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "trust",
		Category:     runtime.CategoryPermissions,
		Description:  "trust this project, so side-effect tools may run here",
		ArgumentHint: "[on|off|once|status]",
		Action: func(args string) string {
			fields := strings.Fields(args)
			verb := ""
			if len(fields) > 0 {
				verb = strings.ToLower(fields[0])
			}
			switch verb {
			case "", "on":
				if err := mgr.SetDecision(cwd, Trusted); err != nil {
					return fmt.Sprintf("golder: could not save trust: %v", err)
				}
				return fmt.Sprintf("trusted %s (saved)", cwd)
			case "off":
				// Clear any active session grant first: IsTrusted checks
				// session before the persisted decision, so without this an
				// "always" granted earlier in the session would keep the dir
				// trusted until restart and the message below would be a lie.
				mgr.ClearSessionTrust(cwd)
				if err := mgr.SetDecision(cwd, Untrusted); err != nil {
					return fmt.Sprintf("golder: could not save trust: %v", err)
				}
				return fmt.Sprintf("marked %s untrusted (saved); tools will ask", cwd)
			case "once":
				mgr.SetSessionTrust(cwd)
				return fmt.Sprintf("trusted %s for this session only (not saved)", cwd)
			case "status":
				res := mgr.NearestTrustDecision(cwd)
				state := fmt.Sprintf("%s: undecided (no saved decision)", cwd)
				if res.Found {
					state = fmt.Sprintf("%s: %s (decision saved for %s)", cwd, res.Decision, res.Path)
				}
				if mgr.ReadsTrusted(cwd) {
					state += "; local reads are allowed for this session"
				}
				return state
			default:
				// From the command's own record, so the line taught here and the hint
				// the popup and /help print are one string.
				return reg.UsageLine("trust") + "  (default: on)"
			}
		},
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "grants",
		Category:     runtime.CategoryPermissions,
		Description:  "view or edit the authorization table directly",
		ArgumentHint: "[list | add <path> <r|w|e> | rm <path>]",
		Action: func(args string) string {
			fields := strings.Fields(args)
			verb := ""
			if len(fields) > 0 {
				verb = strings.ToLower(fields[0])
			}
			switch verb {
			case "", "list":
				// The whole picture, not just the table: /grants is where "every
				// row" lives now that /trust answers only for its own directory.
				// The decisions come first because "may tools run here" is the
				// question people arrive with; the table follows.
				return trustReport(mgr, cwd)
			case "add":
				return grantsAdd(reg, fields[1:])
			case "rm", "remove":
				return grantsRemove(reg, fields[1:])
			default:
				// From the command's own record, so the line taught here and the
				// hint the popup and /help print are one string.
				return reg.UsageLine("grants") + "  (default: list)"
			}
		},
	})
}

// grantsAdd records a path authorization in the unified table.
//
// It is the missing write half of the table for a person at a prompt: the gates
// record what a dialog answered, and this is how someone states the same thing
// directly — including for a path no call has touched yet, which is the case a
// dialog can never cover.
//
// Capabilities are the letters the table prints (r/w/e). Write implies read, and
// "e" (execute) is separate on purpose: it is what directory trust has always
// meant, and folding it into "w" would make "let me edit these files" quietly
// equal to "let anything run here".
func grantsAdd(reg *runtime.SlashRegistry, args []string) string {
	usage := reg.UsageLine("grants")
	if len(args) < 2 {
		return "usage: /grants add <path> <r|w|e|rw|rwe|all>   " + usage
	}
	path := args[0]
	caps, ok := grants.ParseCapability(args[1])
	if !ok {
		return fmt.Sprintf("unknown capability %q; use r, w, e or a combination (rw, rwe, all)", args[1])
	}
	g, err := grants.Add(grants.Grant{
		Path:     path,
		Allow:    caps,
		Scope:    grants.Subtree,
		Lifetime: grants.Remembered,
		Source:   "/grants add",
	})
	if err != nil {
		return fmt.Sprintf("golder: could not add %s: %v", path, err)
	}
	// A grant that could not be written is still granted, for this session. The
	// message says which one happened rather than claiming persistence the store
	// did not achieve.
	if g.Lifetime != grants.Remembered {
		return fmt.Sprintf("%s: %s for this session only (could not save it)", g.Path, g.Allow)
	}
	return fmt.Sprintf("%s: %s (saved)", g.Path, g.Allow)
}

// grantsRemove revokes a path's authorizations, remembered and session. The
// config.toml seed is reported as untouched, because that file belongs to the
// user and rewriting it would drop their comments.
func grantsRemove(reg *runtime.SlashRegistry, args []string) string {
	if len(args) < 1 {
		return "usage: /grants rm <path>   " + reg.UsageLine("trust")
	}
	path := args[0]
	s := grants.Process()
	if s == nil {
		return "authorizations: the table is unavailable in this process"
	}
	rem, err := s.Remove(path)
	if err != nil {
		return fmt.Sprintf("golder: could not remove %s: %v", path, err)
	}
	var parts []string
	if rem.Remembered {
		parts = append(parts, "saved authorization")
	}
	if rem.Session {
		parts = append(parts, "session authorization")
	}
	if !rem.Touched() {
		// Reporting "nothing" is more useful than a bare success: it tells the
		// user the path was never granted here, so they are not left wondering
		// whether the command worked.
		return fmt.Sprintf("no authorization recorded for %s", path)
	}
	msg := "removed " + strings.Join(parts, " and ") + " for " + path
	return msg
}

// trustReport renders the whole trust store: every saved decision, sorted by
// path, then the session-only grants that are not on disk at all. /trust status
// answers "what applies here" by walking up from the working directory; this
// answers "what have I granted", which is the question a user asks before
// revoking something — and the reason it cannot be derived from status alone is
// that a decision saved for a parent applies to every child while being stored
// once.
func trustReport(mgr *Manager, cwd string) string {
	saved, sessionTrust, sessionReads := mgr.Entries()
	var b strings.Builder
	store := mgr.Path()
	if store == "" {
		store = "(trust store disabled)"
	}
	fmt.Fprintf(&b, "trust decisions (%s):", ui.DisplayHomePath(store))
	if len(saved) == 0 {
		b.WriteString("\nnone saved")
	}
	for _, e := range saved {
		fmt.Fprintf(&b, "\n  %-10s %s", e.Decision, ui.DisplayHomePath(e.Path))
		// The directory the report was asked from is called out because it is the
		// one the other subcommands act on, and it is the reason the user is
		// reading the list at all.
		if samePath(e.Path, cwd) {
			b.WriteString("  (this directory)")
		}
	}
	if len(sessionTrust) == 0 && len(sessionReads) == 0 {
		b.WriteString("\nsession: no grants beyond the saved decisions")
	} else {
		b.WriteString("\nsession only (not saved; gone on exit):")
		for _, dir := range sessionTrust {
			fmt.Fprintf(&b, "\n  %-10s %s", "trusted", ui.DisplayHomePath(dir))
		}
		for _, dir := range sessionReads {
			fmt.Fprintf(&b, "\n  %-10s %s", "reads", ui.DisplayHomePath(dir))
		}
	}
	b.WriteString("\n" + grantsReport(cwd))
	b.WriteString("\nset: /trust [on|off|once] <this directory>")
	return b.String()
}

// grantsReport renders the unified authorization table: one row per path with
// what it permits and how long it lasts.
//
// It replaced the pointer to /permissions list. The two commands each had one
// half of the picture — trust answered "may tools run here", permissions
// answered "which paths are readable and writable" — and each named the other
// because neither could answer the whole question. This is that whole answer in
// one place, which is the point of the unified table.
func grantsReport(cwd string) string {
	s := grants.Process()
	var b strings.Builder
	if s == nil {
		return "authorizations: (table unavailable; using the legacy stores only)"
	}
	entries := s.Entries()
	b.WriteString("authorizations (path · capability · lifetime):")
	if len(entries.Remembered) == 0 {
		b.WriteString("\n  none remembered")
	} else {
		b.WriteString("\n  remembered across sessions:")
		for _, g := range entries.Remembered {
			fmt.Fprintf(&b, "\n    %-4s %-12s %s", g.Allow.String(), g.Lifetime, ui.DisplayHomePath(g.Path))
			if samePath(g.Path, cwd) {
				b.WriteString("  (this directory)")
			}
		}
	}
	if len(entries.Session) > 0 {
		b.WriteString("\n  this session only:")
		for _, g := range entries.Session {
			fmt.Fprintf(&b, "\n    %-4s %-12s %s", g.Allow.String(), g.Lifetime, ui.DisplayHomePath(g.Path))
			if samePath(g.Path, cwd) {
				b.WriteString("  (this directory)")
			}
		}
	}
	if s.Path() != "" {
		fmt.Fprintf(&b, "\n  store: %s", ui.DisplayHomePath(s.Path()))
	}
	return b.String()
}

// samePath reports whether two paths name the same directory, comparing cleaned
// absolute spellings so a relative cwd and a saved absolute entry agree.
func samePath(a, b string) bool {
	ca, err := filepath.Abs(filepath.Clean(a))
	if err != nil {
		return false
	}
	cb, err := filepath.Abs(filepath.Clean(b))
	if err != nil {
		return false
	}
	return ca == cb
}

// GateOpts configures the trust gate. Exactly one answer channel is used:
// Confirm when set (the TUI's dialog, or a driver's own routing), otherwise the
// stdin prompt when In and Out are set. A gate with neither fails closed: a
// run with no one to ask refuses the call with guidance instead of guessing.
type GateOpts struct {
	// Manager is the trust store. A nil manager disables the gate (Gate
	// returns nil), matching the pre-existing "trust unavailable" behavior.
	Manager *Manager
	// Cwd is the directory the trust decision is about; it is also the key the
	// session grants are filed under, so every caller must pass the run's
	// workspace rather than a tool argument.
	Cwd string
	// In/Out/Mu are the shared stdin prompt channel (interactive line drivers).
	In  *bufio.Reader
	Out io.Writer
	Mu  *sync.Mutex
	// Confirm is the driver's own approval channel. When set it is preferred
	// over In/Out: it takes the same ApprovalRequest every other gate asks
	// with, so one driver dialog answers trust, read-scope and permission
	// questions alike.
	Confirm func(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer
	// OnPrompt, when set, is told the question just before a prompt is put to
	// the person — through Confirm or through the stdin prompt below — and
	// returns the function to call once it is answered. See judge.GateOpts'
	// field of the same name: it is how a driver tells the terminal around it
	// that this pane waits for a human (internal/hostcompat).
	OnPrompt func(question string) func()
}

// promptNote is GateOpts' half of the OnPrompt contract: a no-op when no hook
// is wired, so the caller can always defer the result.
func (o *GateOpts) promptNote(question string) func() {
	if o.OnPrompt == nil {
		return func() {}
	}
	if done := o.OnPrompt(question); done != nil {
		return done
	}
	return func() {}
}

// Gate builds the trust gate: side-effect tools and, under StrictReads, the
// data-ingestion tools (see GatesTool) are asked about in a directory the
// session has not trusted. In a trusted directory the call is allowed (nil).
// Otherwise the user answers with the ladder - once runs this call, "reads"
// grants session reads for the directory (LocalReadTools only), and "always"
// grants session trust so every tool stops asking - and the chosen grant is
// filed on the manager, so it applies to the next call of any driver sharing
// that manager. A nil Manager returns nil (trust disabled, no gating).
//
// Concurrency: this hook runs concurrently with itself. bash is
// ToolExecutionParallel (codex parity: the shell is shareable and the exclusive
// lock belongs to the editing tools, which apply_patch keeps), so two commands
// in one batch reach the gate at the same time. mu serializes the stdin prompt
// - required now, rather than the insurance it was while every side-effect tool
// was sequential - and it is nil-safe (a driver wiring trust without a mutex
// gets no serialization). A driver that answers through Confirm instead (the
// TUI) queues each request on its own dialog queue, which is where its
// serialization lives, and the manager's own state is mutex-guarded.
func Gate(o GateOpts) agentcore.BeforeToolCallFunc {
	mgr := o.Manager
	if mgr == nil {
		return nil
	}
	cwd := strings.TrimSpace(o.Cwd)
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if !mgr.GatesTool(call.Name, cwd) {
			return nil
		}
		if o.Mu != nil {
			o.Mu.Lock()
			defer o.Mu.Unlock()
		}
		// Check under the lock: a prior call's "always" in the same batch may
		// have granted session trust, in which case this call skips the prompt.
		// (Today batches with side-effect tools run serially, so this is
		// belt-and-suspenders.)
		if mgr.IsTrusted(cwd) {
			return nil
		}
		// The unified table is the other place a "trust this directory" answer
		// can live now: an Execute grant there means the same thing this gate's
		// own store means, so a session that recorded its answer through the
		// newer path is not asked again by the older one.
		//
		// Execute specifically — not Read or Write. This gate asks whether
		// side-effect tools may RUN here, which is what Execute encodes; a
		// directory trusted for reading or writing is a different decision and
		// must not silently authorize execution.
		if grants.Allows(cwd, grants.Execute) {
			return nil
		}
		var ans Answer
		answered := false
		// The pane is about to wait for a person. The note is taken once, here,
		// rather than at each prompt: whether the dialog or the stdin prompt
		// answers, the wait is one wait and it ends in the same place.
		done := o.promptNote("approve " + call.Name + ": " + cwd + " is not trusted yet")
		defer done()
		if o.Confirm != nil {
			// The one request shape every driver dialog answers. The gate owns
			// the request (tool, preview, directory, why) so a driver only
			// supplies the question's transport. An unanswered request - the
			// dialog could not run, or this driver has no client for it right
			// now - falls through to the stdin prompt when one exists.
			a := o.Confirm(ctx, judge.ApprovalRequest{
				Kind:      judge.ApprovalTrust,
				Tool:      call.Name,
				Summary:   ToolCallSummary(call),
				TrustDir:  cwd,
				Rationale: cwd + " is not trusted yet",
			})
			if a.Answered {
				ans, answered = Answer{Allow: a.Approve, Always: a.Always, Reads: a.SessionReads}, true
			}
		}
		if !answered && o.In != nil && o.Out != nil {
			// The prompt is interruptible: a Ctrl+C during the read cancels the
			// run context and the read yields a denial, so the interrupt takes
			// effect immediately instead of waiting for an answer.
			ans, answered = ConfirmToolCall(ctx, o.Out, o.In, call), true
		}
		if ans.Always {
			mgr.SetSessionTrust(cwd)
			// Mirror the answer into the unified table, which is what /trust
			// lists and what the next session loads. Session-scoped, because
			// that is what this answer means here: persisting a directory is
			// the launch picker's job, and a mid-run dialog that quietly wrote
			// to disk would be a decision the user did not make.
			_, _ = grants.Add(grants.Grant{
				Path:     cwd,
				Allow:    grants.Execute,
				Scope:    grants.Subtree,
				Lifetime: grants.Session,
				Source:   "trust dialog",
			})
		}
		if ans.Reads {
			mgr.SetSessionReads(cwd)
			// The narrower answer grants reading, not running: it must not
			// record Execute, or the next call would skip the very question the
			// user just declined to answer in full.
			_, _ = grants.Add(grants.Grant{
				Path:     cwd,
				Allow:    grants.Read,
				Scope:    grants.Subtree,
				Lifetime: grants.Session,
				Source:   "trust dialog (reads)",
			})
		}
		if !ans.Allow {
			return blockUntrusted(call, cwd, o.Confirm != nil || o.In != nil)
		}
		return nil
	}
}

// blockUntrusted refuses a call in a directory the session has not trusted,
// with guidance the model can act on. answered says whether the run had any way
// to ask (a dialog or stdin); with none - a headless or process-isolated run
// that could not settle the call from its authorization snapshot - the message
// says so instead of advising an answer the run cannot take.
func blockUntrusted(call agentcore.AgentToolCall, cwd string, answered bool) *agentcore.BeforeToolCallDecision {
	msg := fmt.Sprintf("tool %q blocked: %s is not trusted (use /trust to trust this project)", call.Name, cwd)
	if !answered {
		msg += " — this run has no prompt, so ask the user to trust the directory in an interactive session"
	}
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}

// Answer is a trust prompt's decision. The positive answers differ in what
// they remember, which the prompt spells out: Allow runs the call once, Always
// stops every tool asking in the directory for the session, and Reads stops
// only the local read tools (LocalReadTools) - reads of the folder's own
// files - while commands and network reads keep asking.
type Answer struct {
	// Allow runs the call; false is a denial (the model is told, and can
	// propose something else).
	Allow bool
	// Always grants session trust for the directory: every tool runs without
	// asking until golder exits. Not persisted.
	Always bool
	// Reads grants local reads for the directory for the session. Not
	// persisted.
	Reads bool
}

// ConfirmToolCall asks whether a trust-gated tool call may run in an untrusted
// directory. Denial (no/empty/EOF/interrupt) returns the zero Answer.
//
// The keys are the TUI dialog's, and deliberately so: this prompt and the
// dialog ask the same question, and they used to answer it with different
// letters (`r` for reads here, `a` for all tools here — where the dialog used
// `a` for reads and `p` for all tools). A person who learned one front-end
// would press the wrong key in the other, and on this question the wrong key is
// a wider grant than intended. Now both spell the same ladder: y once, a reads
// (this session), p all tools (this session).
func ConfirmToolCall(ctx context.Context, out io.Writer, in *bufio.Reader, call agentcore.AgentToolCall) Answer {
	fmt.Fprintf(out, "\ngolder wants to use %q in an untrusted directory.\n", call.Name)
	if summary := toolCallSummary(call); summary != "" {
		fmt.Fprintf(out, "  %s\n", summary)
	}
	// Each answer names its scope, because they differ in what they remember:
	// once keeps nothing, "reads" keeps the folder's file reads, "all" keeps
	// the whole folder. Only a local read tool has the middle rung - on a
	// command it would just be the same answer with a narrower-sounding name.
	reads := LocalReadTools[call.Name]
	if reads {
		fmt.Fprint(out, "Allow? [y]es once / [n]o / [a]llow reads in this folder (this session) / [p] all tools here (this session) [y/N/a/p]: ")
	} else {
		fmt.Fprint(out, "Allow? [y]es once / [n]o / [p] all tools here (this session) [y/N/p]: ")
	}
	line, ok := readLineOrCancel(ctx, in)
	if !ok {
		return Answer{}
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return Answer{Allow: true}
	case "a", "all", "reads":
		if reads {
			fmt.Fprintln(out, "Reads in this folder will not ask again this session; commands and network reads still will.")
			return Answer{Allow: true, Reads: true}
		}
	case "p", "permanent", "always":
		return Answer{Allow: true, Always: true}
	}
	return Answer{}
}

// readLineOrCancel reads one line, returning ok=false when ctx fires first so
// a Ctrl+C during a confirmation prompt denies immediately instead of
// trapping the user until they answer. The orphaned read may deliver late
// input to the next prompt on the shared reader; that matches the existing
// typed-ahead contract (input is never split between prompts).
func readLineOrCancel(ctx context.Context, in *bufio.Reader) (line string, ok bool) {
	type result struct {
		line string
	}
	ch := make(chan result, 1)
	go func() {
		line, _ := in.ReadString('\n')
		ch <- result{line}
	}()
	select {
	case r := <-ch:
		return r.line, true
	case <-ctx.Done():
		return "", false
	}
}

// ToolCallSummary is the exported form of toolCallSummary, used by callers
// outside this package (e.g. the remote-control confirm path) that need the
// same one-line preview a local confirmation prompt shows.
func ToolCallSummary(call agentcore.AgentToolCall) string { return toolCallSummary(call) }

// toolCallSummary renders a one-line preview of what a gated tool will do, so
// the user can make an informed allow/deny choice: the bash command, the paths
// a patch touches, or the file, pattern, URL or query a data-ingestion tool
// reaches for. It is best-effort; if the arguments do not parse, or name
// nothing summarizable, it falls back to a truncated raw view.
func toolCallSummary(call agentcore.AgentToolCall) string {
	raw := strings.TrimSpace(string(call.Arguments))
	if raw == "" || raw == "{}" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return truncateForPrompt(raw)
	}
	switch call.Name {
	case "bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			// The bare command: the prompt line already names the tool, and the
			// TUI renders summaries after "$ ", where a "command:" label would
			// read as "$ command: go test ./...".
			return truncateForPrompt(cmd)
		}
	case "apply_patch":
		if text, ok := args["patch"].(string); ok && text != "" {
			paths := patch.Paths(text)
			if len(paths) == 0 {
				// Unparseable: the tool will reject it, but show the head so
				// the prompt still describes what is being asked for.
				return "patch: " + truncateForPrompt(oneLinePreview(text))
			}
			return "patch touching " + truncateForPrompt(strings.Join(paths, ", "))
		}
	// Data-ingestion tools, gated under StrictReads: name the target instead of
	// asking the user to judge raw JSON.
	case "read":
		if p := argString(args, "path"); p != "" {
			return "read " + truncateForPrompt(p)
		}
	case "view_image":
		if p := argString(args, "path"); p != "" {
			return "view " + truncateForPrompt(p)
		}
	case "ls":
		if p := argString(args, "path"); p != "" {
			return "list " + truncateForPrompt(p)
		}
		return "list the workspace root"
	case "grep":
		if s := searchSummary("grep", "pattern", args); s != "" {
			return s
		}
	case "find":
		if s := searchSummary("find", "glob", args); s != "" {
			return s
		}
	case "webfetch":
		if u := argString(args, "url"); u != "" {
			return "fetch " + truncateForPrompt(u)
		}
	case "websearch":
		if q := argString(args, "query"); q != "" {
			return truncateForPrompt("search \"" + q + "\"")
		}
	}
	return truncateForPrompt(raw)
}

// argString returns a string argument, trimmed, or "" when the key is absent
// or holds something else, so a malformed call falls through to the raw view
// rather than printing a misleading summary.
func argString(args map[string]any, key string) string {
	s, ok := args[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// argPaths renders the search tools' path argument, which accepts one path or
// a list of them (the same shapes the tools' schemas declare).
func argPaths(args map[string]any) string {
	switch v := args["path"].(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// searchSummary renders a grep/find preview: the pattern (or glob) looked for
// and, when the call names a place, where.
func searchSummary(verb, key string, args map[string]any) string {
	pattern := argString(args, key)
	if pattern == "" {
		return ""
	}
	out := verb + " \"" + pattern + "\""
	if where := argPaths(args); where != "" {
		out += " in " + where
	}
	return truncateForPrompt(out)
}

// oneLinePreview collapses a patch's whitespace so the prompt preview stays on
// one line.
func oneLinePreview(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateForPrompt caps a string at maxPromptPreview runes so a confirmation
// prompt stays readable even for large write/edit payloads.
func truncateForPrompt(s string) string {
	const maxPromptPreview = 200
	r := []rune(s)
	if len(r) <= maxPromptPreview {
		return s
	}
	return string(r[:maxPromptPreview]) + " …"
}
