package agenttool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
)

const tuiosMaxScript = 1 << 20

type tuiosScript struct {
	id, receiver, bootstrap, frames string
	receipt                         *regexp.Regexp
}

// newTuiosScript prepares a one-shot POSIX command, not an installed agent or
// service. Data lines are shell comments: if ssh or the receiver exits, queued
// payload cannot become commands in the parent login shell. Every frame must
// arrive in order and the entire encoded body must match length and SHA-256
// before decoding, syntax-checking and starting a noninteractive child shell.
func newTuiosScript(body string, remote ...bool) (*tuiosScript, error) {
	if strings.TrimSpace(body) == "" || len(body) > tuiosMaxScript || strings.ContainsRune(body, 0) {
		return nil, errors.New("script must be nonempty POSIX sh text, contain no NUL, and be at most 1 MiB")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	p := &tuiosScript{id: hex.EncodeToString(nonce[:])}
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	sum := sha256.Sum256([]byte(encoded))
	var frames strings.Builder
	count := 0
	for start := 0; start < len(encoded); start += 512 {
		fmt.Fprintf(&frames, "#G:%s:%d:%s\n", p.id, count, encoded[start:min(start+512, len(encoded))])
		count++
	}
	fmt.Fprintf(&frames, "#G:%s:END\n", p.id)
	p.frames = frames.String()
	scope := ":"
	if len(remote) > 0 && remote[0] {
		// A ready receipt alone is insufficient: ssh might have exited before
		// the setup line reached it, leaving a perfectly valid LOCAL receiver.
		// A Linux SSH context on a different kernel fails closed in that case.
		scope = `test -n "$SSH_CONNECTION" && test -r /proc/sys/kernel/random/boot_id || __gw_fail`
		if runtime.GOOS == "linux" {
			boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
			if err != nil {
				return nil, errors.New("cannot establish local kernel identity for remote script")
			}
			id := strings.TrimSpace(string(boot))
			if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(id) {
				return nil, errors.New("invalid local kernel identity")
			}
			scope += `; test "$(cat /proc/sys/kernel/random/boot_id)" != ` + quoteControlArg(id) + ` || __gw_fail`
		}
	}
	// No model-supplied text is interpolated here. Only a random id, integer
	// lengths and a hex digest enter the small setup line.
	p.receiver = strings.NewReplacer("@ID@", p.id, "@COUNT@", strconv.Itoa(count), "@BYTES@", strconv.Itoa(len(encoded)), "@HASH@", hex.EncodeToString(sum[:]), "@SCOPE@", scope).Replace(tuiosReceiver)
	p.receiver = strings.Join(strings.FieldsFunc(p.receiver, func(r rune) bool { return r == '\n' }), " ")
	p.bootstrap = "sh -c " + quoteControlArg(p.receiver) + "\n"
	// VT captures may wrap at any column. Whitespace between marker characters
	// tolerates soft wraps without matching the quoted/format-string setup echo.
	var marker []string
	for _, r := range "G:" + p.id + ":" {
		marker = append(marker, regexp.QuoteMeta(string(r)))
	}
	p.receipt = regexp.MustCompile(`(?m)^` + strings.Join(marker, `[ \t\r\n]*`) + `[ \t\r\n]*(R|F|D[ \t\r\n]*([0-9])[ \t\r\n]*([0-9])[ \t\r\n]*([0-9]))[ \t]*\r?$`)
	return p, nil
}

const tuiosReceiver = `__gw_n=@ID@; __gw_d=; __gw_st=;
__gw_mark(){ printf '\nG:%s:%s\n' "$__gw_n" "$1"; }; __gw_fail(){ __gw_mark F; exit 125; };
trap 'test -z "$__gw_st" || stty "$__gw_st" 2>/dev/null; test -z "$__gw_d" || rm -rf "$__gw_d"' 0;
trap 'exit 125' 1 2 15;
@SCOPE@;
for __gw_c in sh base64 mktemp wc rm; do command -v "$__gw_c" >/dev/null 2>&1 || __gw_fail; done;
if command -v sha256sum >/dev/null 2>&1; then __gw_hash(){ sha256sum; }; elif command -v shasum >/dev/null 2>&1; then __gw_hash(){ shasum -a 256; }; else __gw_fail; fi;
__gw_d=$(mktemp -d "${TMPDIR:-/tmp}/golder-wire.XXXXXXXXXX") || __gw_fail; __gw_u=$(umask); umask 077;
if test -t 0; then __gw_st=$(stty -g) || __gw_fail; stty -echo || __gw_fail; fi;
__gw_mark R; __gw_i=0; __gw_end=0;
while IFS= read -r __gw_l; do case "$__gw_l" in "#G:$__gw_n:$__gw_i:"*) __gw_v=${__gw_l#"#G:$__gw_n:$__gw_i:"}; printf '%s' "$__gw_v" >>"$__gw_d/p" || __gw_fail; __gw_i=$((__gw_i+1));; "#G:$__gw_n:END") __gw_end=1; break;; *) __gw_fail;; esac; done;
test "$__gw_end" -eq 1 && test "$__gw_i" -eq @COUNT@ || __gw_fail;
test "$(wc -c <"$__gw_d/p")" -eq @BYTES@ || __gw_fail;
__gw_h=$(__gw_hash <"$__gw_d/p") || __gw_fail; test "${__gw_h%% *}" = @HASH@ || __gw_fail;
base64 -d <"$__gw_d/p" >"$__gw_d/s" 2>/dev/null || base64 -D <"$__gw_d/p" >"$__gw_d/s" 2>/dev/null || __gw_fail;
sh -n "$__gw_d/s" || __gw_fail; (umask "$__gw_u"; sh "$__gw_d/s" </dev/null); __gw_rc=$?;
printf '\nG:%s:D%03d\n' "$__gw_n" "$__gw_rc"; exit 0;`

func (p *tuiosScript) parseReceipt(text string) (string, int) {
	matches := p.receipt.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", 0
	}
	m := matches[len(matches)-1]
	switch m[1][0] {
	case 'R':
		return "ready", 0
	case 'F':
		return "rejected", 0
	default:
		code, _ := strconv.Atoi(m[2] + m[3] + m[4])
		if code <= 255 {
			return "completed", code
		}
		return "", 0
	}
}

func (t *TuiosTool) runScript(ctx context.Context, c *tuiosctl.Client, a tuiosArgs) (agentcore.AgentToolResult, error) {
	p, err := newTuiosScript(a.Script, a.Remote)
	if err != nil {
		return tuiosError(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, tuiosTimeout(a.TimeoutMS))
	defer cancel()
	result := func(state string, code int, output string, cause error) (agentcore.AgentToolResult, error) {
		out := map[string]any{"window": a.Window, "transfer_id": p.id, "execution_state": state, "retryable": false, "output": tuiosTruncate(tuiosUntrusted(output))}
		if state == "completed" {
			out["exit_code"] = code
			out["integrity"] = "sha256_checked"
		}
		if cause != nil {
			out["error"] = cause.Error()
			out["recovery"] = "Do not resend automatically. Inspect this exact pane; if the receiver is waiting, a person can interrupt it. A lost completion receipt does not mean the script failed to run."
		}
		return agentcore.AgentToolResult{IsError: state != "completed" || code != 0, Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(out))}, Details: out}, nil
	}
	// Two sends and two receipt-wait phases (plus close subscriptions). There
	// are no per-chunk round trips, screen polling, paste sleeps or retransmits.
	if _, err = c.SendText(ctx, a.Window, p.bootstrap, false); err != nil {
		return result("not_started", 0, "", err)
	}
	wait := func() (string, string, int, error) {
		deadline, _ := ctx.Deadline()
		_, err := t.waitPane(ctx, c, tuiosctl.WaitParams{Condition: "window-output", Window: a.Window, Pattern: p.receipt.String(), Timeout: time.Until(deadline)})
		if err != nil {
			return "", "", 0, err
		}
		cap, err := c.CapturePane(ctx, a.Window, "recent", tuiosMaxLines, false)
		if err != nil {
			return "", "", 0, err
		}
		state, code := p.parseReceipt(cap.Content)
		return cap.Content, state, code, nil
	}
	text, state, code, err := wait()
	if err != nil {
		return result("not_started", 0, text, err)
	}
	if state != "ready" {
		return result("rejected", code, text, errors.New("receiver did not acknowledge readiness; no script payload was sent"))
	}
	if _, err = c.SendText(ctx, a.Window, p.frames, false); err != nil {
		return result("unknown", 0, "", err)
	}
	// The ready receipt remains on screen. Completion must use its own pattern
	// or a wait could immediately match stale READY and invent a result.
	p.receipt = regexp.MustCompile(strings.Replace(p.receipt.String(), "(R|F|D", "(F|D", 1))
	text, state, code, err = wait()
	if err != nil {
		return result("unknown", 0, text, err)
	}
	if state != "completed" && state != "rejected" {
		return result("unknown", 0, text, errors.New("completion receipt could not be read"))
	}
	return result(state, code, text, nil)
}
