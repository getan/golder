// Package diagtrace is golder's one opt-in diagnostic log, shared by the
// process-start path and the full-screen TUI. GOLDER_TUI_TRACE names the file
// (GOLDER_STARTUP_TRACE is an alias); with no file every call is a no-op.
//
// One process clock starts at package initialization, so events from main, run
// assembly, plugins, hooks and the TUI are ordered in one timeline instead of
// two unrelated ones. Each line carries pid, UTC time, elapsed milliseconds and
// a stage tag:
//
//	pid=4242 2026/10/26 12:00:00.000000 +12.345ms setup provider name=... duration=...
//
// Privacy: callers record phases, counts, booleans and durations only. Never
// pass prompts, keystrokes, clipboard contents, file paths, env values, session
// ids or credentials as event text. The one environment dump below is a
// terminal-capability whitelist; everything else records presence, not values.
// Plugin discovery may record an executable's basename, nothing else.
package diagtrace

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// processStart is the clock origin. Package initialization runs before main,
// which is exactly the blind spot a trace started from tui.Run cannot see.
var processStart = time.Now()

// Trace is one open diagnostic destination. It is safe for concurrent use.
type Trace struct {
	mu     sync.Mutex
	log    *log.Logger
	file   *os.File
	path   string
	closed bool
}

// Open opens path for append and records the process capabilities. An existing
// file is reused; its permissions are tightened to 0600 because a diagnostic
// log must never be group/other readable.
func Open(path string) (*Trace, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("diagtrace: private permissions: %w", err)
	}
	t := &Trace{
		log:  log.New(f, fmt.Sprintf("pid=%d ", os.Getpid()), log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC),
		file: f,
		path: path,
	}
	t.Event("diag", "start os=%s arch=%s go=%s", runtime.GOOS, runtime.GOARCH, runtime.Version())
	// Deliberately not os.Environ(): it contains credentials. These names only
	// describe the terminal. Presence (not values) is enough for host variables.
	for _, key := range []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "CLICOLOR", "CLICOLOR_FORCE"} {
		t.Event("diag", "env %s=%q", key, os.Getenv(key))
	}
	for _, key := range []string{"NO_COLOR", "TMUX", "STY", "SSH_TTY", "SSH_CONNECTION", "WT_SESSION"} {
		_, set := os.LookupEnv(key)
		t.Event("diag", "env %s_set=%t", key, set)
	}
	return t, nil
}

// Event writes one line. It is nil-safe and harmless after Close.
func (t *Trace) Event(stage, format string, args ...any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.log.Printf("+%.3fms %s %s", float64(time.Since(processStart))/float64(time.Millisecond),
		strings.TrimSpace(stage), fmt.Sprintf(format, args...))
}

// Close writes the end event and closes the file. Repeated Close is harmless.
func (t *Trace) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.log.Printf("+%.3fms diag trace end", float64(time.Since(processStart))/float64(time.Millisecond))
	_ = t.file.Close()
}

// Path reports the file this trace appends to.
func (t *Trace) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

var (
	processMu   sync.Mutex
	process     *Trace
	initialized bool
)

// PathFromEnv reports the configured trace file, preferring GOLDER_TUI_TRACE
// over the GOLDER_STARTUP_TRACE alias.
func PathFromEnv() string {
	if p := strings.TrimSpace(os.Getenv("GOLDER_TUI_TRACE")); p != "" {
		return p
	}
	return strings.TrimSpace(os.Getenv("GOLDER_STARTUP_TRACE"))
}

// InitFromEnv initializes the process trace from the environment. It is
// idempotent: the first call decides, and later calls neither reopen the file
// nor repeat an earlier failure.
func InitFromEnv() error {
	return Init(PathFromEnv())
}

// Init initializes the process trace at path ("" disables it). It is idempotent:
// the first call wins, so main can initialize early and the TUI can call
// InitFromEnv again without opening a second file or truncating the first.
func Init(path string) error {
	processMu.Lock()
	defer processMu.Unlock()
	if initialized {
		return nil
	}
	initialized = true
	if strings.TrimSpace(path) == "" {
		return nil
	}
	t, err := Open(path)
	if err != nil {
		return err
	}
	process = t
	return nil
}

// Process returns the process trace, or nil when diagnostics are disabled.
func Process() *Trace {
	processMu.Lock()
	defer processMu.Unlock()
	return process
}

// Enabled reports whether anything is being recorded.
func Enabled() bool { return Process() != nil }

// Event writes one process-level event; a disabled trace drops it.
func Event(stage, format string, args ...any) {
	Process().Event(stage, format, args...)
}

// Close closes the process trace. It is safe to call more than once.
func Close() {
	processMu.Lock()
	t := process
	process = nil
	// Allow a later Init (a re-exec in the same process image, or a test) to
	// open a fresh file. A pointer someone still holds is closed and safe.
	initialized = false
	processMu.Unlock()
	t.Close()
}

// Started reports the process clock's origin.
func Started() time.Time { return processStart }
