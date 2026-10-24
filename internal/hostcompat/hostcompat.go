// Package hostcompat reports golder's state to a terminal multiplexer that
// tracks coding agents — golder's own workspace, tuios and herdr — so golder
// shows up beside claude, codex and the rest without any of them shipping an
// integration for it.
//
// Every host accepts a report from the agent itself, through a small contract
// none of them has to change:
//
//   - golder's own workspace takes the same control-protocol verbs tuios does,
//     over its own daemon socket, spoken directly (see workspace.go): a pane
//     finds the daemon through TUIOS_SOCKET and names itself through
//     TUIOS_PANE_ID. No other program is needed: that daemon is golder, and it
//     already knows its own agent.
//   - tuios takes `tuios set-agent-state`, an ordinary control-protocol verb.
//     A pane finds the daemon through TUIOS_SOCKET and names itself through
//     TUIOS_PANE_ID; the state names one of tuios's seven (none, working,
//     needs_input, idle, done, errored, unknown). Being *recognised* as an
//     agent at all is the one thing a report cannot do on its own: that needs
//     a harness manifest, a file, which this package ships and installs (see
//     install.go).
//   - herdr takes `$HERDR_BIN_PATH pane report-agent`. No file: the report
//     names the agent, so it is identified by reporting. State is one of four
//     (working, blocked, idle, unknown) and a resume command rides the report.
//
// The vocabularies and the command lines differ, so this package speaks one
// neutral vocabulary (State) and translates per host. Every decision about
// *which* host a process reports to is made once, by Resolve, so detection, the
// reporter's own name and `golder host-compat status` cannot disagree.
//
// The package is a leaf: apart from internal/tuiosctl — itself standard
// library only, and the owner of the wire protocol the workspace host speaks —
// it imports no golder code, so a driver can hold a *Reporter without dragging
// anything into the call graph.
//
// Everything is optional and silent. Outside a host pane Open returns nil, a
// nil *Reporter answers every call as a no-op, and a report that fails (the
// host is gone, the binary is missing) is dropped: the person's agent must
// never wait on, or break over, bookkeeping for the terminal around it.
package hostcompat

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
)

// State is the neutral state vocabulary the drivers report in. Each host
// translates it; a host that cannot express a state ignores it rather than
// guessing (herdr has no "done", tuios has no "unknown").
type State string

const (
	// StateNone clears the pane's agent: nothing runs there any more. It is a
	// real report, distinct from the zero State, which means "no state change"
	// and is only used by the reporter's own plumbing.
	StateNone State = "none"
	// StateIdle is at rest and ready for a prompt.
	StateIdle State = "idle"
	// StateWorking is mid-turn.
	StateWorking State = "working"
	// StateBlocked is waiting on the person (an approval or a question).
	StateBlocked State = "blocked"
	// StateDone finished its turn. Only a harness can claim this, which is
	// exactly the case here: golder says so itself.
	StateDone State = "done"
	// StateErrored stopped on an error.
	StateErrored State = "errored"
)

// Kind says what a blocked state waits for. tuios records it (and its risk
// rules read the message that goes with it); herdr has no field for it, so the
// message alone carries the reason there.
type Kind string

const (
	KindNone     Kind = ""
	KindApproval Kind = "approval"
)

// Report is one state report. Message is one plain line: for a blocked state
// it should say what golder waits for ("approve Bash: go test ./...").
//
// An empty State means "no state change": the report carries the conversation
// alone (see Reporter.Session).
type Report struct {
	State   State
	Kind    Kind
	Message string
}

// SessionRef is the conversation a pane holds, as reported once per session.
// ResumeArgv is the command that reopens it, used by the hosts that take one
// (herdr); ID is used by both.
type SessionRef struct {
	ID         string
	ResumeArgv []string
}

// Host is one multiplexer's own dialect. Implementations are stateless apart
// from caching the binary they exec.
type Host interface {
	// Name is the host's id, "tuios" or "herdr".
	Name() string
	// Apply sends one state report, with the pane's conversation attached when
	// one is known.
	Apply(ctx context.Context, r Report, sess SessionRef) error
	// ApplyMeta sends display metadata (model, context use, task summary).
	// A host that cannot show metadata reports no error and does nothing.
	ApplyMeta(ctx context.Context, kv map[string]string) error
	// Release clears the pane's agent, for a golder that is going away.
	Release(ctx context.Context) error
}

// Options are the seams a test (or a driver that knows better) can replace.
// Every field is optional; the zero Options reads the real environment and
// execs the real binaries.
type Options struct {
	// Getenv reads the environment (TUIOS_ENV, HERDR_ENV, ...).
	Getenv func(string) string
	// LookPath finds the tuios binary. herdr names its own through
	// HERDR_BIN_PATH, so only the tuios host needs this.
	LookPath func(string) (string, error)
	// Run execs one argv. It exists so the command line each host sends is
	// testable without either host installed.
	Run func(ctx context.Context, argv []string) error
	// Now is the clock behind herdr's monotonic sequence numbers.
	Now func() time.Time
	// Timeout bounds one report. Zero means DefaultTimeout.
	Timeout time.Duration
	// ErrLog receives one line per failed report. Nil discards them: a
	// multiplexer hiccup is not worth interrupting an agent's transcript.
	ErrLog io.Writer
}

// DefaultTimeout bounds one report. A host call is bookkeeping on the side of
// the person's real work, so it is given long enough to reach a local socket
// and no longer.
const DefaultTimeout = 1500 * time.Millisecond

// Reporter is a driver's handle on the host: nil outside a host pane, and
// safe to call in every case. Reports are coalesced and sent on a goroutine of
// their own, so an agent never waits on the terminal around it and a burst of
// state changes costs one command, not one per change.
type Reporter struct {
	host    Host
	run     func(context.Context, []string) error
	timeout time.Duration
	errLog  io.Writer
	now     func() time.Time

	mu      sync.Mutex
	pending pending
	busy    bool
	closed  bool
	wg      sync.WaitGroup
}

// pending is the work not yet sent, one slot per kind: only the latest state
// matters, metadata is merged (a later report wins per key), and a session, a
// release or a close replaces whatever came before it.
type pending struct {
	state   *Report
	meta    map[string]string
	session *SessionRef
	release bool
}

// empty reports whether there is nothing to send.
func (p *pending) empty() bool {
	return p.state == nil && len(p.meta) == 0 && p.session == nil && !p.release
}

// The multiplexers this package knows by name. The three drivers are also host
// names, because the host a report goes to is the multiplexer it is about: the
// same string names the pane's environment, the reporter and the line
// `golder host-compat status` prints.
const (
	// MuxNone is a plain terminal: no multiplexer around this process.
	MuxNone = ""
	// MuxGolder is a pane of golder's own workspace, started by golder itself.
	MuxGolder = "golder-workspace"
	// MuxTuios is a pane of an external tuios daemon.
	MuxTuios = "tuios"
	// MuxHerdr is a herdr pane.
	MuxHerdr = "herdr"
	// MuxTmux, MuxZellij and MuxScreen are multiplexers golder can recognise
	// and cannot report to. They are named so a person is told which terminal
	// they are in rather than only that nothing is reported.
	MuxTmux   = "tmux"
	MuxZellij = "zellij"
	MuxScreen = "screen"
)

// EnvHostReport is the escape hatch: "off" disables reporting everywhere,
// "golder-workspace", "tuios" or "herdr" forces one host (which is how a person
// works around a pane that looks like two, or tests a host from inside
// another). "golder" and "workspace" are accepted as names for golder's own.
const EnvHostReport = "GOLDER_HOST_REPORT"

// EnvWorkspace marks a process started inside golder's own workspace. Unlike
// TUIOS_ENV, which is tuios's published contract and therefore read strictly,
// this marker is golder's own: anything that says "yes" enables it, and "0"
// does not (see enabled).
const EnvWorkspace = "GOLDER_WORKSPACE"

// The variables each pane contract is found through. The workspace driver
// plants the tuios names because the panes it starts run the same tooling
// (internal/tuiosctl reads them), plus the marker that says whose daemon it is.
const (
	envTuiosEnv     = tuiosctl.EnvEnv
	envTuiosSocket  = tuiosctl.EnvSocket
	envTuiosSession = tuiosctl.EnvSession
	envTuiosPaneID  = tuiosctl.EnvPaneID
	envTuiosProof   = tuiosctl.EnvPaneToken

	envHerdrEnv        = "HERDR_ENV"
	envHerdrPaneID     = "HERDR_PANE_ID"
	envHerdrBinPath    = "HERDR_BIN_PATH"
	envHerdrSocketPath = "HERDR_SOCKET_PATH"
)

// requirements are the variables a host needs before it can report about a
// pane, in the order a person reads them. A host missing one of these is not
// opened: reporting from a pane golder cannot name would land on somebody
// else's window, or nowhere.
var requirements = map[string][]string{
	MuxGolder: {envTuiosSocket, envTuiosPaneID},
	MuxTuios:  {envTuiosPaneID},
	MuxHerdr:  {envHerdrPaneID, envHerdrBinPath},
}

// Resolution is one decision about reporting, made from the environment: which
// multiplexer is around this process, which host (if any) will report to it,
// and what is missing when nothing will.
type Resolution struct {
	// Multiplexer is the multiplexer this process runs inside, whether or not
	// this package can report to it. "" in a plain terminal.
	Multiplexer string
	// Host is the dialect that will report, "" when nothing will. It is the
	// name Reporter.HostName returns and the name status prints.
	Host string
	// Candidate is the host Missing is about: the one Resolve would use if the
	// environment were complete. It is the detected multiplexer, or the host
	// GOLDER_HOST_REPORT forced, and it equals Host whenever Host is set.
	Candidate string
	// Forced is GOLDER_HOST_REPORT as the environment set it, "" when unset.
	Forced string
	// Off is set when GOLDER_HOST_REPORT turned reporting off.
	Off bool
	// Missing lists the variables the multiplexer's host needs and the
	// environment does not set. It is empty when the host can report.
	Missing []string
}

// Resolve decides what this process reports to. Detect, Open and `golder
// host-compat status` all read it, so the three cannot drift apart: the host
// named here is the host that is spoken to and the host that is printed.
//
// A pane can look like two hosts at once. golder's own workspace wins: a pane
// it starts carries the tuios variables too (its daemon speaks the same
// protocol), and reporting through an external `tuios` binary that is not
// installed — or is a different daemon — would be wrong. Between the external
// hosts, tuios wins over herdr, and GOLDER_HOST_REPORT settles either by hand.
func Resolve(getenv func(string) string) Resolution {
	res := Resolution{Multiplexer: MuxNone}
	if getenv == nil {
		return res
	}
	res.Multiplexer = detectMultiplexer(getenv)
	res.Forced = strings.TrimSpace(getenv(EnvHostReport))

	mux := res.Multiplexer
	switch res.Forced {
	case "golder", "workspace":
		mux = MuxGolder
	case MuxGolder, MuxTuios, MuxHerdr:
		mux = res.Forced
	case "off", "none", "0", "false":
		res.Off = true
		return res
	}
	need, ok := requirements[mux]
	if !ok {
		// Nothing detected, or a multiplexer with no driver: nothing is
		// reported, and nothing is missing (see MuxTmux and the rest).
		return res
	}
	res.Candidate = mux
	res.Missing = missingVars(need, getenv)
	if len(res.Missing) == 0 {
		res.Host = mux
	}
	return res
}

// Detect names the host this process reports to, or "" when it reports
// nowhere. It reads the environment only, so it is safe to call from anywhere.
func Detect(getenv func(string) string) string { return Resolve(getenv).Host }

// detectMultiplexer names the terminal multiplexer around this process, in
// precedence order. golder's own workspace comes first because its panes carry
// every other variable too; then the two external hosts this package speaks to;
// then the multiplexers it can only recognise.
func detectMultiplexer(getenv func(string) string) string {
	switch {
	case enabled(getenv(EnvWorkspace)):
		return MuxGolder
	case getenv(envTuiosEnv) == "1":
		return MuxTuios
	case getenv(envHerdrEnv) == "1":
		return MuxHerdr
	case getenv("TMUX") != "":
		return MuxTmux
	case getenv("ZELLIJ") != "" || getenv("ZELLIJ_SESSION_NAME") != "":
		return MuxZellij
	case getenv("STY") != "":
		return MuxScreen
	}
	return MuxNone
}

// enabled reads a marker variable golder sets. "0" is not enabled: a wrapper
// that exports GOLDER_WORKSPACE=0 means "no", and reporting into a workspace
// that is not there would be worse than saying nothing.
func enabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// missingVars lists the names in need the environment leaves empty.
func missingVars(need []string, getenv func(string) string) []string {
	var missing []string
	for _, name := range need {
		if strings.TrimSpace(getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// Open returns a reporter for the host this process runs in, or nil when
// Resolve says nothing will report. OpenSession adds the lifecycle bookkeeping
// on top of it, which is what a driver normally holds.
func Open(opts Options) *Reporter {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Run == nil {
		opts.Run = execArgv
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}

	var host Host
	switch res := Resolve(opts.Getenv); res.Host {
	case MuxGolder:
		host = newWorkspace(opts)
	case MuxTuios:
		host = newTuios(opts)
	case MuxHerdr:
		host = newHerdr(opts)
	}
	if host == nil {
		return nil
	}
	return &Reporter{
		host:    host,
		run:     opts.Run,
		timeout: opts.Timeout,
		errLog:  opts.ErrLog,
		now:     opts.Now,
	}
}

// HostName is the host this reporter talks to, "" for a nil reporter (or a
// reporter whose host went away).
func (r *Reporter) HostName() string {
	if r == nil || r.host == nil {
		return ""
	}
	return r.host.Name()
}

// State reports a state change, with kind and message for a blocked one. It is
// safe on a nil reporter, never blocks the caller, and drops a report that
// cannot be sent.
func (r *Reporter) State(s State, kind Kind, message string) {
	if r == nil || r.host == nil {
		return
	}
	rep := Report{State: s, Kind: kind, Message: oneLine(message)}
	r.push(func(p *pending) { p.state = &rep })
}

// Working is State(StateWorking, KindNone, "") — the common case, spelled out.
func (r *Reporter) Working(message string) { r.State(StateWorking, KindNone, message) }

// Blocked is State(StateBlocked, kind, message).
func (r *Reporter) Blocked(kind Kind, message string) { r.State(StateBlocked, kind, message) }

// Done is a finished turn.
func (r *Reporter) Done(message string) { r.State(StateDone, KindNone, message) }

// Session records the conversation a pane holds, so a host can offer to reopen
// it (and so a person reading the rail sees which conversation is running).
// argv is the resume command, used by the hosts that take one.
func (r *Reporter) Session(id string, argv []string) {
	if r == nil || r.host == nil || id == "" {
		return
	}
	ref := SessionRef{ID: id, ResumeArgv: append([]string(nil), argv...)}
	r.push(func(p *pending) { p.session = &ref })
}

// SessionState records the conversation and the state in one pending batch.
// Start used to call Session then State; under a slow scheduler the sender
// could drain the conversation alone between the two calls, leaving a
// session-only report before the idle state and breaking the one-report
// contract callers observe after Start.
func (r *Reporter) SessionState(id string, argv []string, s State, kind Kind, message string) {
	if r == nil || r.host == nil {
		return
	}
	ref := SessionRef{ID: id, ResumeArgv: append([]string(nil), argv...)}
	rep := Report{State: s, Kind: kind, Message: oneLine(message)}
	r.push(func(p *pending) {
		p.session = &ref
		p.state = &rep
	})
}

// Meta reports display metadata: model, context use, a one-line summary of the
// task. A host without a field for it ignores the call.
func (r *Reporter) Meta(kv map[string]string) {
	if r == nil || r.host == nil || len(kv) == 0 {
		return
	}
	r.push(func(p *pending) {
		if p.meta == nil {
			p.meta = make(map[string]string, len(kv))
		}
		for k, v := range kv {
			p.meta[k] = oneLine(v)
		}
	})
}

// Release clears the pane's agent: golder is going away. It replaces anything
// still queued (a release after a state report is the same news, later) and is
// flushed by Close.
func (r *Reporter) Release() {
	if r == nil || r.host == nil {
		return
	}
	r.push(func(p *pending) {
		p.state = nil
		p.meta = nil
		p.release = true
	})
}

// clientHost is a host that holds a connection to its multiplexer: the
// workspace host speaks the protocol directly and keeps the socket open between
// reports, and Close is where it gives that socket back.
type clientHost interface {
	Close() error
}

// Close flushes what is queued and stops the reporter. It is safe to call more
// than once and safe on a nil reporter.
//
// Close waits, bounded by Timeout, because it is normally called on the way
// out of the process: a release that is merely queued when the process exits
// is a pane the host believes still holds a running agent.
func (r *Reporter) Close() {
	if r == nil || r.host == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
	// After the last report, never before it: the socket the release went over
	// is this one.
	if c, ok := r.host.(clientHost); ok {
		_ = c.Close()
	}
}

// push merges work into the pending slot and starts the sender when it is not
// already running. The merge runs under the lock, so two goroutines reporting
// at once cannot lose a report.
func (r *Reporter) push(merge func(*pending)) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	merge(&r.pending)
	start := !r.busy
	if start {
		r.busy = true
		r.wg.Add(1)
	}
	r.mu.Unlock()
	if start {
		go r.send()
	}
}

// send drains the pending slot until it is empty. One report in flight at a
// time, and the newest answer to "what is this pane doing" always wins.
func (r *Reporter) send() {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		work := r.pending
		r.pending = pending{}
		if work.empty() {
			r.busy = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		r.apply(work)
	}
}

// apply sends one drained batch, in the order a reader would expect: the
// conversation first (a state report carries it), then metadata, then either
// the release or the state, since a release and a state are the same field.
func (r *Reporter) apply(work pending) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	var sess SessionRef
	if work.session != nil {
		sess = *work.session
	}
	if work.release {
		r.report(ctx, "release", r.host.Release(ctx))
		return
	}
	// A state report carries the conversation, so a host that restarted between
	// the two does not lose it; a report with no state is the conversation
	// alone.
	rep := Report{}
	if work.state != nil {
		rep = *work.state
	}
	if work.state != nil || work.session != nil {
		r.report(ctx, "state "+string(rep.State), r.host.Apply(ctx, rep, sess))
	}
	if len(work.meta) > 0 {
		r.report(ctx, "meta", r.host.ApplyMeta(ctx, work.meta))
	}
}

// report logs one failure when a log is wired. It never returns the error: the
// caller is an agent mid-turn and has nothing useful to do with it.
func (r *Reporter) report(ctx context.Context, what string, err error) {
	if err == nil || r.errLog == nil {
		return
	}
	msg := err.Error()
	if ctx.Err() != nil {
		msg = "timed out"
	}
	_, _ = io.WriteString(r.errLog, "golder: host report ("+r.host.Name()+" "+what+"): "+msg+"\n")
}

// execArgv runs one argv with the child's output discarded: the reports are
// fire-and-forget, and a host that prints a complaint has nowhere to print it
// but into the agent's own screen.
func execArgv(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// oneLine flattens text into the single line a host shows, cutting it well
// inside every host's own limit. A newline in a report would break the line
// the host prints under the pane's name.
func oneLine(s string) string {
	const maxRunes = 200

	var b []rune
	space := false
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			r = ' '
		}
		if r == ' ' {
			// A run of spaces collapses to one, and a leading one is dropped:
			// both hosts print this line under a pane's name, where a ragged
			// edge reads as a formatting bug. A trailing space is dropped at
			// the end instead, so the common case never has to look ahead.
			if space || len(b) == 0 {
				continue
			}
			space = true
		} else {
			space = false
		}
		b = append(b, r)
		if len(b) == maxRunes {
			return string(b) + "…"
		}
	}
	// Cut the one trailing space a collapsed run can leave behind.
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	return string(b)
}
