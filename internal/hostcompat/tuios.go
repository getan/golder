package hostcompat

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// The tuios dialect.
//
// tuios takes state through `tuios set-agent-state`, an ordinary
// control-protocol verb: the command dials the daemon over TUIOS_SOCKET, and
// the pane names itself with -w "$TUIOS_PANE_ID" rather than relying on which
// window the person has focused.
//
// The binary is exec'd rather than the socket spoken to directly. A bare call
// on the socket has to identify its pane through the token TUIOS_PANE_TOKEN
// carries, and a connection that does not present it is held to the focused
// pane instead of the caller's own — which is the one mistake that would put a
// report on somebody else's window. The CLI does that handshake, and it is the
// same program the person would run by hand.
//
// States map one to one: tuios's vocabulary is the superset of this package's,
// which is why the neutral vocabulary looks like it does.

// tuiosHost reports through the tuios CLI.
type tuiosHost struct {
	bin    string
	pane   string
	runner func(context.Context, []string) error
}

// newTuios returns the tuios host for this process, or nil when the pane's
// environment or the binary is missing.
func newTuios(opts Options) Host {
	pane := opts.Getenv("TUIOS_PANE_ID")
	if pane == "" {
		// A pane always has an id. Without one this is a tuios client process
		// rather than a pane's own shell, which is not a place to report from.
		return nil
	}
	bin := findBinary(opts, "tuios")
	if bin == "" {
		return nil
	}
	return &tuiosHost{bin: bin, pane: pane, runner: opts.Run}
}

func (h *tuiosHost) Name() string { return "tuios" }

// tuiosStates maps the neutral vocabulary onto tuios's own. Every state is
// expressible, so this is a plain rename.
var tuiosStates = map[State]string{
	StateNone:    "none",
	StateIdle:    "idle",
	StateWorking: "working",
	StateBlocked: "needs_input",
	StateDone:    "done",
	StateErrored: "errored",
}

func (h *tuiosHost) Apply(ctx context.Context, r Report, sess SessionRef) error {
	if r.State == "" {
		// No state: the conversation alone, through the verb tuios keeps for
		// exactly that (a report that named a state would move the pane).
		return h.setSession(ctx, sess)
	}
	state, ok := tuiosStates[r.State]
	if !ok {
		return nil
	}
	argv := []string{
		h.bin, "set-agent-state", state,
		"-w", h.pane,
		"--harness", Harness,
	}
	if r.Kind != KindNone {
		argv = append(argv, "--kind", string(r.Kind))
	}
	if r.Message != "" {
		argv = append(argv, "-m", r.Message)
	}
	if sess.ID != "" && r.State != StateNone {
		argv = append(argv, "--agent-session-id", sess.ID)
	}
	return h.runner(ctx, argv)
}

// setSession names the conversation without touching the pane's state.
func (h *tuiosHost) setSession(ctx context.Context, sess SessionRef) error {
	if sess.ID == "" {
		return nil
	}
	return h.runner(ctx, []string{
		h.bin, "set-agent-session",
		"-w", h.pane,
		"--harness", Harness,
		sess.ID,
	})
}

// tuiosMetaKeys are the metadata keys tuios draws under the agent's row, in
// the order they are sent. The neutral vocabulary is already tuios's: model,
// context and summary are what its rail reads, and cost is what a status-line
// feed reports.
//
// A slice rather than a map for a reason that outlives readability: a report's
// command line is part of what this package promises, so the order must not
// depend on Go's randomised map iteration.
var tuiosMetaKeys = []string{"model", "context", "summary", "cost"}

// tuiosMetaMaxValue is tuios's own cut on one value, in characters.
const tuiosMetaMaxValue = 80

func (h *tuiosHost) ApplyMeta(ctx context.Context, kv map[string]string) error {
	argv := []string{
		h.bin, "set-agent-meta",
		"-w", h.pane,
		"--source", Harness,
		// A feed that stops writing must not leave a stale model or context
		// reading on the rail, so every value carries a lifetime; the reporter
		// refreshes it well inside this.
		"--ttl", "90s",
	}
	n := 0
	for _, k := range tuiosMetaKeys {
		v, ok := kv[k]
		if !ok || v == "" {
			continue
		}
		argv = append(argv, k+"="+cutRunes(v, tuiosMetaMaxValue))
		n++
	}
	if n == 0 {
		return nil
	}
	return h.runner(ctx, argv)
}

func (h *tuiosHost) Release(ctx context.Context) error {
	// Clearing the state is tuios's release: the pane goes back to being a
	// shell, which is exactly what a golder that has gone away leaves behind.
	return h.runner(ctx, []string{
		h.bin, "set-agent-state", "none",
		"-w", h.pane,
		"--harness", Harness,
	})
}

// findBinary locates a host program: the path this machine has, or nothing.
// A missing program means no reporting, never an error the agent has to carry.
func findBinary(opts Options, name string) string {
	look := opts.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(name)
	if err != nil || path == "" {
		return ""
	}
	return path
}

// cutRunes cuts s to at most n characters, since the hosts count characters
// and a byte cut would split a multi-byte rune.
func cutRunes(s string, n int) string {
	if len(s) <= n { // the common case: no multi-byte rune can make this longer
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Harness is the id golder reports itself under, everywhere: it is the harness
// manifest's id for tuios, and the agent label herdr shows beside the pane.
const Harness = "golder"

// sessionIDMax bounds a reported conversation id. Both hosts keep a short
// token; a golder session id is a timestamp and a counter, well inside it.
const sessionIDMax = 128

// ValidSessionID reports whether a session id may be reported: the hosts put
// it on a command line (tuios types a resume command from it, herdr holds it
// to its own rules), so it is restricted to what every shell and both hosts
// read as one plain token.
func ValidSessionID(id string) bool {
	if id == "" || len(id) > sessionIDMax || id[0] == '-' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// ResumeArgv is the command that reopens a golder session. It is what herdr
// types into a restored pane; tuios builds the same command from the harness
// manifest's [resume] block, so the two stay in step through this function and
// that file.
//
// The id is checked here rather than trusted: it arrives from the session
// store, and the command is typed at a shell.
func ResumeArgv(id string) []string {
	if !ValidSessionID(id) {
		return nil
	}
	return []string{Harness, "--resume", id}
}

// resumeArgvString renders an argv for a host that wants one string, quoting
// only what needs it. A golder resume command never does, so this exists for
// the general case and returns "" for an argv that cannot be rendered safely.
func resumeArgvString(argv []string) string {
	var b strings.Builder
	for i, tok := range argv {
		if tok == "" || strings.ContainsAny(tok, " \t\n\"'\\$`&|;<>()*?[]{}!#~") {
			return ""
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(tok)
	}
	return b.String()
}

// metaContext renders a context reading as a percentage, which is the form
// both hosts show.
func metaContext(used, window int) string {
	if window <= 0 {
		return ""
	}
	pct := used * 100 / window
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return strconv.Itoa(pct) + "%"
}
