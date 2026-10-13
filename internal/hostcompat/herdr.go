package hostcompat

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// The herdr dialect.
//
// herdr takes state through `$HERDR_BIN_PATH pane report-agent`, its own CLI
// and socket. What tuios needs a manifest file for, herdr gets from the report
// itself: the report names the agent, so golder is recognised by reporting.
// That is herdr's documented path for a vendor-owned integration (see "Add
// Herdr support to your agent"), and the reason this package needs no
// detection manifest on herdr's side at all.
//
// Three things differ from tuios and are handled here:
//
//   - A sequence number. herdr drops a report whose seq is not above the last
//     one it accepted from the same source, so a late report cannot overwrite a
//     newer state. The clock in nanoseconds is monotonic across a golder
//     restart, which a counter starting at 1 would not be.
//   - A narrower vocabulary. herdr knows idle, working, blocked and unknown;
//     it has no "done" and no "errored". Both collapse onto idle — the pane is
//     at rest and nothing is asked of the person — and the message says which
//     it was, so nothing is invented and nothing is lost silently.
//   - A resume command in the report. herdr types it into the restored pane,
//     so golder's own resume argv rides along instead of being read out of a
//     manifest.

// herdrHost reports through herdr's own binary.
type herdrHost struct {
	bin    string
	pane   string
	source string
	runner func(context.Context, []string) error
	now    func() time.Time

	// mu guards seq, which must only ever go up: two reports racing on the
	// same nanosecond would otherwise be dropped as out of order.
	mu  sync.Mutex
	seq uint64
}

// herdrSource is the integration id golder reports under. herdr keeps its own
// integrations in the "herdr:" namespace, and the docs ask a vendor's
// integration to use something else.
const herdrSource = Harness

// newHerdr returns the herdr host for this process, or nil when the pane's
// environment is missing. Unlike tuios there is no PATH lookup: herdr names
// the binary that owns the pane, which is also the binary that must answer.
func newHerdr(opts Options) Host {
	bin := opts.Getenv("HERDR_BIN_PATH")
	pane := opts.Getenv("HERDR_PANE_ID")
	if bin == "" || pane == "" {
		return nil
	}
	return &herdrHost{
		bin:    bin,
		pane:   pane,
		source: herdrSource,
		runner: opts.Run,
		now:    opts.Now,
	}
}

func (h *herdrHost) Name() string { return "herdr" }

// herdrStates maps the neutral vocabulary onto herdr's four. done and errored
// become idle because that is what they are to herdr's model — the pane is not
// working and does not want a person — while the message keeps what happened.
var herdrStates = map[State]string{
	StateNone:    "unknown",
	StateIdle:    "idle",
	StateWorking: "working",
	StateBlocked: "blocked",
	StateDone:    "idle",
	StateErrored: "idle",
}

// nextSeq returns the next sequence number for a report. The clock is the
// source and the stored value only ever moves forward, so a clock that steps
// backwards (or a report that raced) still cannot produce a seq herdr would
// drop as stale.
func (h *herdrHost) nextSeq() uint64 {
	now := uint64(h.now().UnixNano())
	h.mu.Lock()
	defer h.mu.Unlock()
	if now <= h.seq {
		now = h.seq + 1
	}
	h.seq = now
	return now
}

func (h *herdrHost) Apply(ctx context.Context, r Report, sess SessionRef) error {
	// A report about a session and nothing else rides the verb herdr keeps for
	// it, which does not move the pane's state. It is checked before the state
	// table because an empty state names no state on purpose, and looking it up
	// would drop the report — and with it the conversation id herdr needs to
	// reopen this pane.
	if r.State == "" {
		return h.reportSession(ctx, sess)
	}
	state, ok := herdrStates[r.State]
	if !ok {
		return nil
	}
	argv := []string{
		h.bin, "pane", "report-agent", h.pane,
		"--source", h.source,
		"--agent", Harness,
		"--state", state,
		"--seq", strconv.FormatUint(h.nextSeq(), 10),
	}
	if msg := herdrMessage(r); msg != "" {
		argv = append(argv, "--message", msg)
	}
	if sess.ID != "" {
		argv = append(argv, "--agent-session-id", sess.ID)
	}
	// The resume command, after the separator herdr uses for it.
	if len(sess.ResumeArgv) > 0 {
		argv = append(argv, "--")
		argv = append(argv, sess.ResumeArgv...)
	}
	return h.runner(ctx, argv)
}

// herdrMessage is what herdr shows beside the pane: the report's own message,
// or, for a state herdr cannot name, the word for it. A done turn with no
// message still says "done", which is the difference between golder finishing
// a task and golder sitting at an empty prompt.
func herdrMessage(r Report) string {
	if r.Message != "" {
		return r.Message
	}
	switch r.State {
	case StateDone:
		return "turn finished"
	case StateErrored:
		return "stopped on an error"
	}
	return ""
}

// reportSession names the conversation and its resume command without moving
// the pane's state.
func (h *herdrHost) reportSession(ctx context.Context, sess SessionRef) error {
	if sess.ID == "" {
		return nil
	}
	argv := []string{
		h.bin, "pane", "report-agent-session", h.pane,
		"--source", h.source,
		"--agent", Harness,
		"--seq", strconv.FormatUint(h.nextSeq(), 10),
		"--agent-session-id", sess.ID,
	}
	if len(sess.ResumeArgv) > 0 {
		argv = append(argv, "--")
		argv = append(argv, sess.ResumeArgv...)
	}
	return h.runner(ctx, argv)
}

// herdrMetaKeys maps the neutral metadata vocabulary onto the tokens herdr
// shows under the pane. herdr takes arbitrary token names, so this is the
// short, stable set worth a row.
var herdrMetaKeys = []string{"model", "context", "summary"}

// herdrMetaTTL is how long a metadata token lives. Like tuios's, it exists so
// a feed that stops writing leaves nothing stale behind.
const herdrMetaTTL = 90 * time.Second

func (h *herdrHost) ApplyMeta(ctx context.Context, kv map[string]string) error {
	argv := []string{
		h.bin, "pane", "report-metadata", h.pane,
		"--source", h.source,
		"--agent", Harness,
		"--seq", strconv.FormatUint(h.nextSeq(), 10),
		"--ttl-ms", strconv.FormatInt(herdrMetaTTL.Milliseconds(), 10),
	}
	n := 0
	for _, k := range herdrMetaKeys {
		v, ok := kv[k]
		if !ok || v == "" {
			continue
		}
		argv = append(argv, "--token", k+"="+v)
		n++
	}
	if n == 0 {
		return nil
	}
	return h.runner(ctx, argv)
}

func (h *herdrHost) Release(ctx context.Context) error {
	// herdr has a release verb, and it is worth using rather than reporting
	// unknown: it clears the name, the state and the resume command at once,
	// where a state report would leave the pane named golder until herdr
	// noticed the shell prompt.
	return h.runner(ctx, []string{
		h.bin, "pane", "release-agent", h.pane,
		"--source", h.source,
		"--agent", Harness,
		"--seq", strconv.FormatUint(h.nextSeq(), 10),
	})
}

// herdrResumeArgvError reports why an argv cannot be sent to herdr, for the
// diagnostic. Nothing in golder can build a bad one (see ResumeArgv), so this
// is the guard against a future caller handing one in.
func herdrResumeArgvError(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	if len(argv) > 64 {
		return fmt.Errorf("resume command has %d arguments, and herdr takes 64", len(argv))
	}
	for _, tok := range argv {
		if containsAny(tok, "\x00\n\r\t") {
			return fmt.Errorf("resume command contains a control character")
		}
	}
	if stub := resumeArgvString(argv); stub == "" {
		return fmt.Errorf("resume command cannot be rendered as one line")
	}
	return nil
}

// containsAny reports whether s holds any byte of set.
func containsAny(s, set string) bool {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(set); j++ {
			if s[i] == set[j] {
				return true
			}
		}
	}
	return false
}
