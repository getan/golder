// This file implements the `!command` shell passthrough shared by the TUI and
// the REPL. A passthrough command runs locally — no model call, no approval,
// no sandbox; it is the user's explicit full-access escape hatch, mirroring
// codex's user shell command. Its output streams back to the caller, and once
// it settles the command plus its exit result is recorded in the conversation
// as a user-role <user_shell_command> message, so the model sees exactly what
// the user ran and what came back.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/execsess"
)

const (
	// userShellTimeout is the passthrough command's hard deadline. It is a
	// backstop, not a policy: codex gives user shell commands an "arbitrarily
	// large" one-hour expiration, and a user-typed command is expected to be
	// interruptible with Ctrl+C rather than to time out.
	userShellTimeout = time.Hour

	// userShellContextBudget caps the output embedded in the conversation
	// record. A chatty command must never flow into the model window whole;
	// past this size the head and tail survive with a "[truncated N bytes]"
	// marker between them, the same idiom the bash tool uses.
	userShellContextBudget = 30_000

	// userShellDisplayBudget caps the output a TUI card renders. It matches the
	// context budget: the card shows what the model saw, not more.
	userShellDisplayBudget = userShellContextBudget

	// userShellHeadBytes / userShellTailBytes bound the in-memory window the
	// observer buffers while the command streams. The dropped middle is
	// reported by a marker, exactly like execsess's own session buffer.
	userShellHeadBytes = 256 << 10
	userShellTailBytes = 256 << 10
)

// The conversation record markers, copied from codex's
// <user_shell_command> contextual user fragment so both tools agree on the
// shape a session export carries.
const (
	userShellOpenMarker  = "<user_shell_command>"
	userShellCloseMarker = "</user_shell_command>"
)

// UserShellOutcome is a settled passthrough command.
type UserShellOutcome struct {
	// Command is the command line the user typed, without the leading "!".
	Command string
	// ExitCode is the process exit code (-1 when the command was aborted).
	ExitCode int
	// Duration is the wall-clock run time (zero when aborted, mirroring codex).
	Duration time.Duration
	// Output is the command's combined output, bounded by the in-memory window.
	Output string
	// TimedOut reports the one-hour backstop fired and the process was killed.
	TimedOut bool
	// Aborted reports the user interrupted the command (Ctrl+C / Esc).
	Aborted bool
}

// shellOutput is a concurrency-safe bounded output window: the first head and
// the last tail bytes are retained and any dropped middle is reported as a
// "[truncated N bytes]" marker. The execsess writer goroutine appends through
// Write while the UI goroutine reads through String, so every method locks.
type shellOutput struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	omitted int
}

func (o *shellOutput) Write(chunk string) {
	if chunk == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.head) < userShellHeadBytes {
		n := userShellHeadBytes - len(o.head)
		if n > len(chunk) {
			n = len(chunk)
		}
		o.head = append(o.head, chunk[:n]...)
		chunk = chunk[n:]
	}
	if chunk == "" {
		return
	}
	o.tail = append(o.tail, chunk...)
	if len(o.tail) > userShellTailBytes {
		drop := len(o.tail) - userShellTailBytes
		o.omitted += drop
		o.tail = append(o.tail[:0], o.tail[drop:]...)
	}
}

func (o *shellOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var b strings.Builder
	b.Grow(len(o.head) + len(o.tail) + 32)
	b.Write(o.head)
	if o.omitted > 0 {
		fmt.Fprintf(&b, "\n[truncated %d bytes]\n", o.omitted)
	}
	b.Write(o.tail)
	return b.String()
}

// UserShellRun is one in-flight passthrough command. The process lives on the
// shared execsess manager, so it is covered by the same LRU bookkeeping and
// kill-on-exit as the agent's own shells.
type UserShellRun struct {
	// Command is the command line being run (without the leading "!").
	Command string

	sess    *execsess.Session
	out     *shellOutput
	started time.Time
	timeout time.Duration
}

// StartUserShell spawns command under the login shell in cwd. It is the `!`
// escape hatch: no sandbox, no approval, no model call.
func StartUserShell(mgr *execsess.Manager, cwd, command string) (*UserShellRun, error) {
	if mgr == nil {
		return nil, errors.New("shell sessions are unavailable (the shell tool is disabled)")
	}
	out := &shellOutput{}
	run := &UserShellRun{
		Command: command,
		out:     out,
		started: time.Now(),
		timeout: userShellTimeout,
	}
	sess, err := mgr.Start(execsess.StartRequest{
		Command:  command,
		Argv:     userShellArgv(command),
		Dir:      cwd,
		Prefix:   "shell",
		Timeout:  userShellTimeout,
		Observer: func(chunk string) { out.Write(chunk) },
	})
	if err != nil {
		return nil, err
	}
	run.sess = sess
	return run, nil
}

// Output returns everything buffered so far: the running UI polls this while
// the command streams.
func (u *UserShellRun) Output() string { return u.out.String() }

// Done reports whether the process has exited.
func (u *UserShellRun) Done() bool { return u.sess == nil || u.sess.Exited() }

// Wait blocks until the process exits, the timeout fires, or ctx is
// cancelled. A cancelled ctx is the user's Ctrl+C: the command is killed and
// the outcome reports codex's aborted shape (exit code -1, "command aborted
// by user", zero duration).
func (u *UserShellRun) Wait(ctx context.Context) UserShellOutcome {
	u.sess.Wait(ctx, u.timeout+5*time.Second, execsess.WaitExit)
	if ctx.Err() != nil {
		u.sess.Kill()
		u.sess.Wait(context.Background(), 5*time.Second, execsess.WaitExit)
		return UserShellOutcome{
			Command:  u.Command,
			ExitCode: -1,
			Output:   "command aborted by user",
			Aborted:  true,
		}
	}
	snap := u.sess.Snapshot()
	outcome := UserShellOutcome{
		Command:  u.Command,
		ExitCode: snap.ExitCode,
		Output:   u.out.String(),
		TimedOut: snap.TimedOut,
	}
	if !snap.StartedAt.IsZero() && !snap.FinishedAt.IsZero() {
		outcome.Duration = snap.FinishedAt.Sub(snap.StartedAt)
	}
	return outcome
}

// userShellArgv builds the argv for a passthrough command. Unlike the bash
// tool (which runs `<shell> -c`), the passthrough prefers a *login* shell —
// codex does the same — so the command sees the PATH and environment the
// user's own terminal has. $SHELL wins; on Windows we probe bash, then
// PowerShell, then cmd exactly like the bash tool does.
func userShellArgv(command string) []string {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return []string{p, "-c", command}
		}
		if p, err := exec.LookPath("powershell"); err == nil {
			return []string{p, "-Command", command}
		}
		return []string{"cmd", "/C", command}
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}
	return []string{shell, "-lc", command}
}

// UserShellRecord renders the conversation record for a settled passthrough
// command: codex's user-role <user_shell_command> fragment verbatim, so the
// model sees the command, its exit code, duration, and output in the shape it
// already knows from codex sessions.
func UserShellRecord(command string, exitCode int, d time.Duration, output string) string {
	return fmt.Sprintf(
		"%s\n<command>\n%s\n</command>\n<result>\nExit code: %d\nDuration: %.4f seconds\nOutput:\n%s\n</result>\n%s",
		userShellOpenMarker, command, exitCode, d.Seconds(), output, userShellCloseMarker)
}

// UserShellMessage wraps UserShellRecord as the user-role message appended to
// the conversation. The output is capped first, so a chatty command can never
// flow into the model window whole.
func UserShellMessage(command string, exitCode int, d time.Duration, output string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{
			agentcore.NewTextContent(UserShellRecord(command, exitCode, d, truncateUserShellOutput(output, userShellContextBudget))),
		},
	}
}

// UserShellDisplayOutput caps the output a UI card renders. Callers pass the
// raw buffered output; the returned text is safe to split into display rows.
func UserShellDisplayOutput(output string) string {
	return truncateUserShellOutput(output, userShellDisplayBudget)
}

// UserShellRecordData is the parsed form of a <user_shell_command> message.
type UserShellRecordData struct {
	Command  string
	ExitCode int
	Output   string
}

var userShellExitCodeRe = regexp.MustCompile(`(?m)^Exit code: (-?\d+)$`)

// ParseUserShellRecord recognizes a conversation message written by
// UserShellRecord and extracts its command, exit code, and output. ok is false
// for every other message, so replay paths render a record as the shell card
// it was instead of dumping raw XML into the transcript.
func ParseUserShellRecord(text string) (UserShellRecordData, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, userShellOpenMarker) || !strings.HasSuffix(text, userShellCloseMarker) {
		return UserShellRecordData{}, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(text, userShellOpenMarker), userShellCloseMarker)
	const cmdOpen, cmdClose = "<command>\n", "\n</command>\n"
	i := strings.Index(body, cmdOpen)
	if i < 0 {
		return UserShellRecordData{}, false
	}
	rest := body[i+len(cmdOpen):]
	j := strings.Index(rest, cmdClose)
	if j < 0 {
		return UserShellRecordData{}, false
	}
	command := rest[:j]
	rest = rest[j+len(cmdClose):]
	const resOpen, resClose = "<result>\n", "\n</result>"
	i = strings.Index(rest, resOpen)
	if i < 0 {
		return UserShellRecordData{}, false
	}
	rest = rest[i+len(resOpen):]
	j = strings.LastIndex(rest, resClose)
	if j < 0 {
		return UserShellRecordData{}, false
	}
	result := rest[:j]
	exitCode := 0
	if m := userShellExitCodeRe.FindStringSubmatch(result); m != nil {
		exitCode, _ = strconv.Atoi(m[1])
	}
	output := result
	const outHeader = "\nOutput:\n"
	if i = strings.Index(result, outHeader); i >= 0 {
		output = result[i+len(outHeader):]
	}
	return UserShellRecordData{Command: command, ExitCode: exitCode, Output: output}, true
}

// truncateUserShellOutput caps s at budget bytes, keeping a head and a tail
// preview (split evenly) joined by a "[truncated N bytes]" marker so both the
// start and the end of the text survive. It is the same head+tail idiom the
// bash tool's truncateToBudget uses; cut points are pulled back to UTF-8 rune
// boundaries so no partial rune is emitted.
func truncateUserShellOutput(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	half := budget / 2
	head := trimUTF8Suffix(s[:half])
	tail := trimUTF8Prefix(s[len(s)-half:])
	removed := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n[truncated %d bytes]\n", removed) + tail
}

// trimUTF8Suffix drops trailing bytes of s that form an incomplete rune.
func trimUTF8Suffix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// trimUTF8Prefix drops leading bytes of s that form an incomplete rune.
func trimUTF8Prefix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[1:]
			continue
		}
		break
	}
	return s
}
