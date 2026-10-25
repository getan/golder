// Package workspacecmd implements `golder workspace`: the command line that
// manages global named terminal sessions, each with an independent daemon.
//
//	golder workspace [run] [flags] [-- <agent args>...]   start it, and attach
//	golder workspace status [flags]                       one JSON status document
//	golder workspace stop [flags]                         shut it down
//
// The package is the argv-and-identity half of the command and nothing more.
// It parses the flags, resolves a global name and its config/state/log/socket
// paths, and decides whether starting one here would
// nest it inside another multiplexer; then it hands one resolved Options value
// to a Backend, which owns the daemon. That split is why this file has no
// os.Exit, no process-wide chdir, no environment mutation, no signal
// subscription and no dependency outside the standard library plus
// internal/hostcompat and internal/golderhome: a policy that can only be
// tested against a real terminal is a policy that is not tested.
//
// Three decisions are worth stating, because they are what the flags mean:
//
//   - Visible by default. `golder workspace` and `golder workspace run` start a
//     workspace a person can see and use. --headless is the explicit "daemon
//     only" mode: it starts the same workspace, attaches nothing, and prints
//     one readiness JSON document so a script can reach it.
//   - Arguments after -- are an argv. They are handed to the golder agent this
//     workspace hosts as tokens, never joined into a string and never re-parsed
//     by a shell. `golder workspace -- --model opus` passes two arguments, and
//     `golder workspace -- 'a; rm -rf b'` passes one literal one.
//   - Names, not invocation directories, select a session. Config, state, log
//     and ownership proof are per name under GOLDER_HOME; cwd is only the saved
//     directory where its initial process starts.
package workspacecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"
)

// Exit codes Run returns. They are exported because the dispatcher in
// cmd/golder passes the code straight to os.Exit and a script may want to name
// them; nothing else in this package exits a process.
const (
	// ExitOK is success, including a help request.
	ExitOK = 0
	// ExitFailure is a workspace that could not be started, reached or
	// stopped: a backend error, an unusable state root, a refused nesting, a
	// backend that is not wired into this build.
	ExitFailure = 1
	// ExitUsage is argv this command cannot act on: an unknown flag, a flag
	// without its value, an extra argument, a session name the daemon would
	// refuse, a --socket path that cannot be a Unix socket.
	ExitUsage = 2
	// ExitInterrupted is a cancellation: the context was already done before
	// the backend was asked, or the backend returned because it was cancelled.
	// It is 128+SIGINT, the code a shell reports for a Ctrl+C'd command.
	ExitInterrupted = 130
)

// Command is the operation one `golder workspace` invocation asks for.
type Command string

const (
	// CommandRun starts a workspace: visible and attached unless
	// Options.Headless, which starts the daemon alone.
	CommandRun Command = "run"
	// CommandNew starts a workspace only if the named session does not exist.
	CommandNew Command = "new"
	// CommandAttach connects to a running session and never starts one.
	CommandAttach Command = "attach"
	// CommandList reports every named session on this machine.
	CommandList Command = "list"
	// CommandStatus reports a running workspace as one JSON document.
	CommandStatus Command = "status"
	// CommandStop stops a running workspace.
	CommandStop Command = "stop"
	// CommandRemove deletes a stopped session's record and directories.
	CommandRemove Command = "rm"
)

// Appearance is the small subset of the workspace's display settings the
// command line accepts. A zero Appearance asks for the configured defaults,
// which is what every field means when it is unset: there is a flag to turn
// icons and animations off and none to turn them back on.
type Appearance struct {
	// ASCIIOnly draws panes without Nerd Font glyphs.
	ASCIIOnly bool
	// NoAnimations disables the workspace's UI animations.
	NoAnimations bool
	// Theme is a colour theme name, "" for the configured one.
	Theme string
}

// StartResult is what a backend reports once a workspace is up. It exists so a
// backend can name what actually happened — a daemon that generated its own
// session name, a socket the runtime chose — without the caller having to
// re-derive it.
type StartResult struct {
	// Session is the session the workspace is running under. Empty keeps the
	// name the options asked for.
	Session string `json:"session"`
	// Socket is the daemon socket the workspace listens on. Empty keeps the
	// path the options named.
	Socket   string `json:"socket"`
	Cwd      string `json:"cwd"`
	Config   string `json:"config"`
	StateDir string `json:"state_dir"`
	Log      string `json:"log"`
	// Created and Instance distinguish the daemon this call started from an
	// existing owner. A failed client may clean up only its own new instance.
	Created  bool   `json:"created"`
	Instance string `json:"instance"`
}

// Backend is the workspace implementation behind the command: the daemon, its
// panes and its client. It is an interface here, and only here, so that the
// command line can be read and tested without a terminal, a daemon or a
// binary on PATH.
//
// The command owns the argv surface, the project identity, the paths and the
// exit codes; a backend owns everything that touches the machine. Every method
// receives the same fully resolved Options, and must respect Headless (start
// the daemon and return, attach nothing) and Shell (no initial agent).
type Backend interface {
	// Start starts the workspace Options describes and returns once it is
	// ready to use. In visible mode it also attaches or opens a window on it,
	// which normally means Start returns when the person leaves.
	Start(ctx context.Context, opts Options) (StartResult, error)
	// Status returns the workspace's status as one JSON document. The command
	// re-indents and validates it; it does not interpret it.
	Status(ctx context.Context, opts Options) ([]byte, error)
	// Stop stops the workspace and its daemon. Stopping a workspace that is
	// not running is a backend's decision; the usual answer is success.
	Stop(ctx context.Context, opts Options) error
}

// Deps are the seams a test (or a driver that knows better) can replace. Every
// field is optional; a zero Deps reads the real environment and the real
// working directory.
type Deps struct {
	// Getenv reads the environment: GOLDER_HOME, the runtime directory and the
	// multiplexer markers hostcompat parses.
	Getenv func(string) string
	// Getwd is the project directory when --cwd was not given.
	Getwd func() (string, error)
}

// Run executes one `golder workspace` invocation with args — the words after
// `workspace` in argv — writing its output to stdout and its errors to stderr.
// It returns the process exit code (see the Exit constants) and never exits,
// changes directory or touches the environment itself.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, backend Backend) int {
	return RunWith(ctx, args, stdout, stderr, backend, Deps{})
}

// RunWith is Run with the environment seams named, which is what a test needs
// to state a GOLDER_HOME and a multiplexer instead of inheriting one.
func RunWith(ctx context.Context, args []string, stdout, stderr io.Writer, backend Backend, deps Deps) int {
	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}
	if deps.Getwd == nil {
		deps.Getwd = os.Getwd
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p, err := parse(args)
	if err != nil {
		return fail(stderr, CommandRun, err)
	}
	// Help wins over everything below it: asking how a command works must not
	// start a workspace, refuse one, or need a state root.
	if p.help {
		_, _ = io.WriteString(stdout, usage)
		return ExitOK
	}
	// A cancelled context is checked before anything on the machine is read,
	// so an interrupted invocation leaves no directory behind and asks no
	// backend to start a daemon it will have to stop.
	if err := ctx.Err(); err != nil {
		return interrupted(stderr, err)
	}

	opts, err := resolve(p, deps)
	if err != nil {
		return fail(stderr, p.command, err)
	}
	if backend == nil {
		return fail(stderr, p.command, errors.New("no workspace backend is wired into this build"))
	}

	switch p.command {
	case CommandList:
		return runList(ctx, opts, stdout, stderr, backend)
	case CommandStatus:
		return runStatus(ctx, opts, stdout, stderr, backend)
	case CommandStop:
		return runStop(ctx, opts, stdout, stderr, backend)
	case CommandRemove:
		return runRemove(ctx, opts, stdout, stderr, backend)
	default:
		return runStart(ctx, opts, stdout, stderr, backend)
	}
}

// runStart starts a workspace and, in headless mode, prints the readiness
// document. A visible start prints nothing on success: its output is the
// workspace itself, and a line typed before the client attaches would land in
// the middle of the screen the person is about to look at.
func runStart(ctx context.Context, opts Options, stdout, stderr io.Writer, backend Backend) int {
	res, err := backend.Start(ctx, opts)
	if err != nil {
		return backendFail(ctx, stderr, opts.Command, err)
	}
	started := opts.reporting(res)
	if !started.Headless {
		return ExitOK
	}
	if err := writeReadiness(stdout, started, res); err != nil {
		return fail(stderr, CommandRun, err)
	}
	return ExitOK
}

// Remover is the optional backend operation `workspace rm` needs. Keeping it
// outside Backend lets a test or another backend omit removal without growing a
// no-op method.
type Remover interface {
	Remove(context.Context, Options) error
}

// runRemove deletes a stopped session's record and directories. It is
// destructive, so the backend proves the daemon is stopped before anything is
// deleted.
func runRemove(ctx context.Context, opts Options, stdout, stderr io.Writer, backend Backend) int {
	remover, ok := backend.(Remover)
	if !ok {
		return fail(stderr, CommandRemove, errors.New("this workspace backend cannot remove sessions"))
	}
	if err := remover.Remove(ctx, opts); err != nil {
		return backendFail(ctx, stderr, CommandRemove, err)
	}
	_, _ = fmt.Fprintf(stdout, "removed workspace %q\n", opts.Session)
	return ExitOK
}

// listedSession is one row of `workspace list` and one object of its --json
// document. The JSON names are the contract: name, default, legacy, running,
// state, panes, cwd and error.
type listedSession struct {
	Name     string `json:"name"`
	Selector string `json:"selector"`
	Default  bool   `json:"default"`
	Legacy   bool   `json:"legacy"`
	Running  *bool  `json:"running,omitempty"`
	State    string `json:"state"`
	Panes    *int   `json:"panes,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Error    string `json:"error,omitempty"`
}

// runList reports every session under the state root. It never starts a daemon
// and never treats an unreachable endpoint as a stopped one: those are
// different answers, and a script acting on "stopped" would start a second
// daemon over a live one.
func runList(ctx context.Context, opts Options, stdout, stderr io.Writer, backend Backend) int {
	sessions, err := Discover(opts.Home)
	if err != nil {
		return fail(stderr, CommandList, err)
	}
	rows := make([]listedSession, len(sessions))
	jobs := make(chan int, len(sessions))
	for i := range sessions {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(sessions)) {
		wg.Go(func() {
			for i := range jobs {
				rows[i] = listRow(ctx, sessions[i], backend)
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return interrupted(stderr, ctx.Err())
	}
	if opts.JSON {
		body, err := json.MarshalIndent(struct {
			Sessions []listedSession `json:"sessions"`
		}{Sessions: rows}, "", "  ")
		if err != nil {
			return fail(stderr, CommandList, err)
		}
		body = append(body, '\n')
		if _, err := stdout.Write(body); err != nil {
			return fail(stderr, CommandList, err)
		}
		return ExitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tPANES\tSTART DIR")
	for _, row := range rows {
		name := row.Selector
		if row.Legacy {
			name += " (legacy)"
		}
		panes := "-"
		if row.Panes != nil {
			panes = fmt.Sprint(*row.Panes)
		}
		dir := row.Cwd
		if strings.TrimSpace(dir) == "" {
			dir = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", listCell(name), row.State, panes, listCell(dir))
	}
	if err := tw.Flush(); err != nil {
		return fail(stderr, CommandList, err)
	}
	return ExitOK
}

func listRow(ctx context.Context, session Options, backend Backend) listedSession {
	row := listedSession{Name: session.Session, Selector: session.Selector(),
		Default: session.Session == defaultSessionName && !session.Legacy,
		Legacy:  session.Legacy, Cwd: session.Cwd, State: "unreachable"}
	if session.DiscoveryError != "" {
		row.Error = session.DiscoveryError
		return row
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	doc, err := backend.Status(probe, session)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	var status struct {
		Running *bool  `json:"running"`
		Cwd     string `json:"cwd"`
		Panes   struct {
			Windows []json.RawMessage `json:"windows"`
		} `json:"panes"`
	}
	if json.Unmarshal(doc, &status) != nil || status.Running == nil {
		row.Error = "invalid status document: expected a running boolean"
		return row
	}
	row.Running = status.Running
	if status.Cwd != "" {
		row.Cwd = status.Cwd
	}
	if *status.Running {
		row.State = "running"
		count := len(status.Panes.Windows)
		row.Panes = &count
	} else {
		row.State = "stopped"
	}
	return row
}

func listCell(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// runStatus forwards the backend's status document. The command re-indents it
// and checks that it is JSON at all: a status command whose output a script
// cannot parse is worse than one that fails.
func runStatus(ctx context.Context, opts Options, stdout, stderr io.Writer, backend Backend) int {
	doc, err := backend.Status(ctx, opts)
	if err != nil {
		return backendFail(ctx, stderr, CommandStatus, err)
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		return fail(stderr, CommandStatus, errors.New("the workspace backend returned no status document"))
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(doc), "", "  "); err != nil {
		return fail(stderr, CommandStatus, fmt.Errorf("the workspace backend returned a status document that is not JSON: %w", err))
	}
	buf.WriteByte('\n')
	if _, err := stdout.Write(buf.Bytes()); err != nil {
		return fail(stderr, CommandStatus, err)
	}
	return ExitOK
}

// runStop stops a workspace and says which one: "stop" is a destructive verb,
// and a person who typed the wrong --session should be able to see it.
func runStop(ctx context.Context, opts Options, stdout, stderr io.Writer, backend Backend) int {
	if err := backend.Stop(ctx, opts); err != nil {
		return backendFail(ctx, stderr, CommandStop, err)
	}
	// The workspace is stopped whether or not this line reaches a screen, so a
	// failed write is not a failed stop: it is not worth a non-zero exit code
	// that a script would read as "the stop did not happen".
	_, _ = fmt.Fprintf(stdout, "stopped workspace %q\n", opts.Session)
	return ExitOK
}

// readiness is the JSON document --headless prints: everything a script needs
// to reach the workspace it just started, and nothing it does not. It is
// written on one line group under a stable set of names, so a caller can read
// it with jq without reading this package.
type readiness struct {
	Session   string `json:"session"`
	Socket    string `json:"socket"`
	Cwd       string `json:"cwd"`
	Project   string `json:"project"`
	ProjectID string `json:"project_id,omitempty"`
	Manifest  string `json:"manifest,omitempty"`
	Config    string `json:"config"`
	StateDir  string `json:"state_dir"`
	Log       string `json:"log"`
	Headless  bool   `json:"headless"`
	Shell     bool   `json:"shell"`
	Nested    bool   `json:"nested,omitempty"`
	Legacy    bool   `json:"legacy,omitempty"`
}

// writeReadiness prints the readiness document for opts: one JSON object and a
// newline, so it composes in a pipeline.
func writeReadiness(w io.Writer, opts Options, result StartResult) error {
	doc := readiness{
		Session:   opts.Session,
		Socket:    opts.Socket,
		Cwd:       opts.Cwd,
		Project:   opts.Project,
		ProjectID: opts.ProjectID,
		Manifest:  opts.ManifestPath,
		Config:    opts.ConfigPath,
		StateDir:  opts.StateDir,
		Log:       opts.LogFile,
		Headless:  opts.Headless,
		Shell:     opts.Shell,
		Nested:    opts.Nested,
		Legacy:    opts.Legacy,
	}
	body, err := json.Marshal(struct {
		readiness
		Created  bool   `json:"created"`
		Instance string `json:"instance,omitempty"`
	}{readiness: doc, Created: result.Created, Instance: result.Instance})
	if err != nil {
		return fmt.Errorf("encode the readiness document: %w", err)
	}
	body = append(body, '\n')
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}

// backendFail is the one place a backend error becomes an exit code and a line
// of text. A backend that returned because its context was cancelled did not
// fail: the person interrupted it, and the code says so.
func backendFail(ctx context.Context, stderr io.Writer, command Command, err error) int {
	if ctx.Err() != nil {
		return interrupted(stderr, ctx.Err())
	}
	return fail(stderr, command, err)
}

// interrupted reports a cancellation, which is not a failure and is given the
// code a shell uses for it.
func interrupted(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "golder workspace: interrupted: %v\n", err)
	return ExitInterrupted
}

// fail writes one error line and returns the exit code its kind deserves: argv
// this command cannot act on is usage (2) and points at the help; anything else
// is a failure (1).
func fail(stderr io.Writer, command Command, err error) int {
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "golder workspace: %v\n%s", err, usageHint)
		return ExitUsage
	}
	fmt.Fprintf(stderr, "golder workspace%s: %v\n", commandSuffix(command), err)
	return ExitFailure
}

// commandSuffix names the subcommand in an error line, so "golder workspace
// status: ..." cannot be mistaken for a failure to start one. A plain start is
// the default and reads without a word.
func commandSuffix(command Command) string {
	if command == "" || command == CommandRun {
		return ""
	}
	return " " + string(command)
}

// usageHint follows a usage error. The whole synopsis would bury the message a
// person needs; the flag list is one command away.
const usageHint = "run 'golder workspace --help' for usage\n"

// usage is the help text. It is also the contract: every flag in it is parsed
// by parse, and the two must not drift.
const usage = `usage: golder workspace [run] [-s <name>] [flags] [-- <agent args>...]
       golder workspace new <name> [flags] [-- <agent args>...]
       golder workspace attach [<name>] [appearance flags]
       golder workspace list [--json]
       golder workspace status [<name>]
       golder workspace stop [<name>]
       golder workspace rm [<name>]
       golder workspace attach|status|stop --legacy <project-id>/<session>

Sessions are global and independent: a name selects one daemon, while the
directory only selects where that session's first agent starts. The default
name is "default". With no subcommand the command starts the named session if
needed and attaches to it. 'new' refuses an existing name, 'attach' never
starts one, 'list' reports every session, and 'rm' deletes a stopped one.

Flags:
  -s, --session <name>   named session (default: default)
  -C, --cwd <dir>        start directory for a new session (default: the
                         current one); on a stopped session it updates the
                         saved directory
      --socket <path>    daemon socket recorded with a new session (default: a
                         private path under the runtime directory)
      --headless         start the daemon only and print one readiness JSON
                         document instead of attaching
      --shell            start with a shell pane and no agent
      --nested           start even inside another multiplexer (golder's own
                         workspace, tuios, herdr, tmux, zellij or screen)
      --config <path>    per-session config file (default: in the session
                         directory under the state root)
      --state-dir <dir>  per-session state directory (default: beside the config)
      --ascii-only       draw panes without Nerd Font glyphs
      --no-animations    disable UI animations
      --theme <name>     colour theme
      --json             machine-readable output for 'list'
      --legacy <spec>    address an old <project-id>/<session> workspace
  -h, --help             print this help and exit

Arguments after -- are passed to the golder agent as an argv, never joined into
a shell command line:

  golder workspace -- --model opus

Exit codes: 0 success, 1 the workspace could not be started, reached or stopped,
2 usage (an unknown flag, a missing value, an extra argument), 130 interrupted.
`
