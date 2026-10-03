// Package execsess manages shell sessions: OS processes that outlive a single
// tool call so their output can be polled and their process group controlled
// (interrupt / kill). It is a leaf package depending only on the standard
// library: internal/agenttool adapts sessions to the model tool surface
// (bash / write_stdin) and the cli layer owns lifecycle (kill-on-exit) without
// an import cycle.
//
// A session is created by Manager.Start and lives until it exits, the manager
// prunes it under capacity pressure, or KillAll runs at shutdown. Output is
// streamed into a bounded head+tail buffer, so a chatty command can never grow
// a session without limit; a reader polls the retained window with ReadNew,
// which reports any dropped middle as a single "[truncated N bytes]" marker.
package execsess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// maxSessions caps concurrently tracked sessions per manager. It is a var so
// tests can lower the cap without spawning dozens of processes.
var maxSessions = 64

// OutputCapBytes bounds the output retained per session (a head and tail
// window). Retained bytes can briefly reach 1.5x this budget because the tail
// is compacted at 2x its share; the bound is what matters, not the exact size.
const OutputCapBytes = 1 << 20

// Status is a session's lifecycle state.
type Status string

const (
	// StatusRunning means the process is still executing.
	StatusRunning Status = "running"
	// StatusExited means the process finished (successfully or not) or was
	// killed.
	StatusExited Status = "exited"
)

// StartRequest configures one session spawn.
type StartRequest struct {
	// Command is the original shell command line, kept for display and for
	// callers that need to re-state what a session is running.
	Command string
	// Argv is the executable and its arguments to spawn (for the bash tool,
	// already wrapped for the platform shell and any sandbox runner).
	Argv []string
	// Dir is the working directory; empty uses the process CWD.
	Dir string
	// Prefix is the session id prefix; empty means "bash".
	Prefix string
	// Timeout, when positive, is a hard deadline: the session is killed when
	// it elapses and Snapshot reports TimedOut.
	Timeout time.Duration
	// Cleanup, when non-nil, runs once after the process exits (e.g. removing
	// a generated sandbox profile). It runs even when Start fails.
	Cleanup func()
	// Observer, when non-nil, receives each output chunk as it is written.
	// It is called from the writer goroutine without the session lock, so it
	// must be quick and must not call back into the session.
	Observer func(string)
	// TTY runs the command on a pseudo-terminal: stdin becomes writable with
	// arbitrary input, stdout/stderr are merged by the terminal, and a
	// conventional 24x80 window is reported. The default (false) is the pipe
	// session used for plain commands.
	TTY bool
	// Env holds extra "KEY=VALUE" entries merged into the process environment
	// (after the inherited one). TTY sessions add their own sanitation
	// entries; callers rarely set this directly.
	Env []string
}

// Snapshot is a copy of a session's state at one instant.
type Snapshot struct {
	ID         string
	Command    string
	Status     Status
	ExitCode   int
	Err        string
	TimedOut   bool
	Timeout    time.Duration
	StartedAt  time.Time
	FinishedAt time.Time
}

// WaitMode selects what a Wait call returns early for.
type WaitMode int

const (
	// WaitExit waits for the process to exit or the timeout/ctx to elapse.
	WaitExit WaitMode = iota
	// WaitOutput additionally returns when output newer than the reader's
	// cursor arrived.
	WaitOutput
)

// Manager owns a set of sessions and assigns their ids.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	seq      int
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// Start spawns req.Argv, registers the session under a fresh id, and returns
// it. When the manager is at capacity it first prunes the oldest exited
// session; if every session is still running the spawn fails rather than
// killing live work.
func (m *Manager) Start(req StartRequest) (*Session, error) {
	if len(req.Argv) == 0 {
		cleanup(req)
		return nil, errors.New("execsess: empty argv")
	}
	m.mu.Lock()
	m.pruneLocked()
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		cleanup(req)
		return nil, fmt.Errorf("execsess: too many running sessions (%d)", maxSessions)
	}
	m.seq++
	prefix := req.Prefix
	if prefix == "" {
		prefix = "bash"
	}
	s := newSession(fmt.Sprintf("%s_%d", prefix, m.seq), req)
	m.sessions[s.ID] = s
	m.mu.Unlock()

	if err := s.start(); err != nil {
		m.mu.Lock()
		delete(m.sessions, s.ID)
		m.mu.Unlock()
		cleanup(req)
		return nil, err
	}
	return s, nil
}

// Get returns the session with the given id, or (nil, false).
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// KillAll terminates every session that is still running. It is the
// shutdown hook drivers call so long-running commands are not orphaned.
func (m *Manager) KillAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Kill()
	}
}

// pruneLocked drops the oldest exited session when at capacity. Lock held.
func (m *Manager) pruneLocked() {
	if len(m.sessions) < maxSessions {
		return
	}
	var oldest *Session
	for _, s := range m.sessions {
		if !s.Exited() {
			continue
		}
		if oldest == nil || s.StartedAt.Before(oldest.StartedAt) {
			oldest = s
		}
	}
	if oldest != nil {
		delete(m.sessions, oldest.ID)
	}
}

func cleanup(req StartRequest) {
	if req.Cleanup != nil {
		req.Cleanup()
	}
}

// ttyEnv returns the process environment for a tty session: the inherited
// environment plus the terminal-sanitizing entries codex applies to unified
// exec (plain, colorless output; no pager trickery), with caller overrides
// applied last.
func ttyEnv(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra)+8)
	out = append(out, base...)
	out = append(out,
		"TERM=dumb",
		"NO_COLOR=1",
		"COLORTERM=",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"GH_PAGER=cat",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	)
	out = append(out, extra...)
	return out
}

// Session is one managed process: its identity, bounded output, and state.
// All fields are guarded by mu; the process's writers, readers, and controls
// may touch it concurrently.
type Session struct {
	// ID is the stable handle (e.g. "bash_2") callers address.
	ID string
	// Command is the original shell command line.
	Command string
	// StartedAt is when the session was registered.
	StartedAt time.Time

	argv     []string
	dir      string
	timeout  time.Duration
	observer func(string)
	tty      bool
	env      []string

	mu       sync.Mutex
	cmd      *exec.Cmd
	pid      int
	buf      *headTailBuffer
	cursor   int64
	status   Status
	exitCode int
	errMsg   string
	timedOut bool
	finished time.Time
	cleanup  func()
	// master is the pty master file for tty sessions, nil otherwise; pumpDone
	// closes once the master pump has drained after the process exits.
	master   *os.File
	pumpDone chan struct{}
	// interrupts counts write-interrupts (Ctrl-C) delivered to the session:
	// the first sends SIGINT, a second escalates to Kill.
	interrupts int
	notify     chan struct{}
	exitedCh   chan struct{}
}

func newSession(id string, req StartRequest) *Session {
	return &Session{
		ID:        id,
		Command:   req.Command,
		StartedAt: time.Now(),
		argv:      req.Argv,
		dir:       req.Dir,
		timeout:   req.Timeout,
		observer:  req.Observer,
		tty:       req.TTY,
		env:       req.Env,
		buf:       newHeadTailBuffer(OutputCapBytes),
		status:    StatusRunning,
		cleanup:   req.Cleanup,
		notify:    make(chan struct{}, 1),
		exitedCh:  make(chan struct{}),
	}
}

// start spawns the process and begins reaping it.
func (s *Session) start() error {
	cmd := exec.Command(s.argv[0], s.argv[1:]...)
	if s.dir != "" {
		cmd.Dir = s.dir
	}
	var slave *os.File
	if s.tty {
		master, sl, err := openPTY()
		if err != nil {
			return err
		}
		slave = sl
		s.master = master
		s.pumpDone = make(chan struct{})
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave
		// A session (setsid + controlling terminal) is what makes the slave a
		// tty for job control; the process group equals the session leader, so
		// group-wide signals below still reach forked children.
		configureTTYProcess(cmd)
		cmd.Env = ttyEnv(os.Environ(), s.env)
	} else {
		configureProcessGroup(cmd)
		// Both streams append to the same bounded buffer through Write, so
		// output ordering is best-effort exactly as it is on a terminal.
		cmd.Stdout = s
		cmd.Stderr = s
		if len(s.env) > 0 {
			cmd.Env = append(os.Environ(), s.env...)
		}
	}
	if err := cmd.Start(); err != nil {
		if s.master != nil {
			s.master.Close()
		}
		if slave != nil {
			slave.Close()
		}
		return err
	}
	if slave != nil {
		// The child holds the slave as its stdio + controlling terminal; the
		// parent must drop its copy so the master sees EOF/EIO when the child
		// has fully exited.
		slave.Close()
	}
	s.mu.Lock()
	s.cmd = cmd
	s.pid = cmd.Process.Pid
	s.mu.Unlock()
	if s.tty {
		go s.pump()
	}
	go s.reap(cmd)
	if s.timeout > 0 {
		go s.enforceTimeout()
	}
	return nil
}

// reap waits for the process and records its terminal state.
func (s *Session) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	exitCode := 0
	errMsg := ""
	if err != nil {
		exitCode = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		errMsg = err.Error()
	}
	s.mu.Lock()
	s.status = StatusExited
	s.exitCode = exitCode
	s.errMsg = errMsg
	s.finished = time.Now()
	cleanupFn := s.cleanup
	s.cleanup = nil
	s.mu.Unlock()
	if s.pumpDone != nil {
		// Wait for the pty pump to drain the post-exit bytes (EOF/EIO), with a
		// short cap so a stray master writer cannot pin reaping (codex uses the
		// same 50ms close-wait bound).
		select {
		case <-s.pumpDone:
		case <-time.After(terminationGracePeriod):
		}
	}
	if cleanupFn != nil {
		cleanupFn()
	}
	close(s.exitedCh)
}

// pump copies the pty master into the session's bounded output buffer until
// the terminal closes (EOF or EIO after the last slave holder exits).
func (s *Session) pump() {
	defer close(s.pumpDone)
	defer s.master.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			_, _ = s.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// IsTTY reports whether the session runs on a pseudo-terminal (its stdin is
// writable with arbitrary input).
func (s *Session) IsTTY() bool { return s.tty }

// SendInput writes raw bytes to a tty session's stdin (for example a line of
// input, or a bare "\n" as an Enter keypress). It fails for pipe sessions and
// for sessions that have already exited. The echo of the input appears in the
// session output, exactly as it would on a terminal.
func (s *Session) SendInput(text string) error {
	s.mu.Lock()
	master := s.master
	running := s.status == StatusRunning
	s.mu.Unlock()
	if !s.tty || master == nil {
		return errors.New("execsess: session has no tty")
	}
	if !running {
		return errors.New("execsess: session has exited")
	}
	_, err := master.WriteString(text)
	return err
}

// enforceTimeout kills the session once its hard deadline elapses.
func (s *Session) enforceTimeout() {
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case <-s.exitedCh:
		return
	case <-timer.C:
	}
	s.mu.Lock()
	if s.status == StatusRunning {
		s.timedOut = true
	}
	s.mu.Unlock()
	s.Kill()
}

// Write implements io.Writer: it appends one output chunk to the bounded
// buffer, forwards it to the observer (if any), and wakes waiting readers.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	obs := s.observer
	s.mu.Unlock()
	if obs != nil {
		obs(string(p))
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

// ReadNew returns output produced since the previous ReadNew, inserting a
// "[truncated N bytes]" marker where the bounded buffer dropped a middle
// region. It advances the reader cursor.
func (s *Session) ReadNew() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, tail, omitted, next := s.buf.readFrom(s.cursor)
	s.cursor = next
	if omitted == 0 {
		return string(head) + string(tail)
	}
	var b strings.Builder
	b.Write(head)
	fmt.Fprintf(&b, "\n[truncated %d bytes]\n", omitted)
	b.Write(tail)
	return b.String()
}

// SetObserver replaces the output observer. Passing nil detaches it; an
// in-flight call for a chunk already being written may still complete.
func (s *Session) SetObserver(fn func(string)) {
	s.mu.Lock()
	s.observer = fn
	s.mu.Unlock()
}

// Snapshot returns a copy of the session's current state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		ID:         s.ID,
		Command:    s.Command,
		Status:     s.status,
		ExitCode:   s.exitCode,
		Err:        s.errMsg,
		TimedOut:   s.timedOut,
		Timeout:    s.timeout,
		StartedAt:  s.StartedAt,
		FinishedAt: s.finished,
	}
}

// Exited reports whether the process has finished.
func (s *Session) Exited() bool {
	select {
	case <-s.exitedCh:
		return true
	default:
		return false
	}
}

// Wait blocks until the process exits, the deadline elapses, or ctx is done.
// With WaitOutput it also returns once output newer than the reader's cursor
// arrived. It reports whether the process has exited.
func (s *Session) Wait(ctx context.Context, d time.Duration, mode WaitMode) bool {
	if s.Exited() {
		return true
	}
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-s.exitedCh:
			return true
		case <-ctx.Done():
			return s.Exited()
		case <-timer.C:
			return s.Exited()
		case <-s.notify:
			if mode == WaitOutput && s.PendingOutput() {
				return s.Exited()
			}
		}
	}
}

// PendingOutput reports whether unread output is buffered.
func (s *Session) PendingOutput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.written() > s.cursor
}

// Interrupt delivers Ctrl-C to the session: the first call sends SIGINT to the
// process group; a second escalates to Kill so a process that ignores SIGINT
// cannot pin the session forever.
func (s *Session) Interrupt() {
	s.mu.Lock()
	if s.status != StatusRunning {
		s.mu.Unlock()
		return
	}
	s.interrupts++
	first := s.interrupts == 1
	pid := s.pid
	s.mu.Unlock()
	if first && pid > 0 && interruptProcessGroup(pid) {
		return
	}
	s.Kill()
}

// Kill terminates the process group: SIGTERM, a short grace period, then
// SIGKILL. It blocks until the process has been reaped.
func (s *Session) Kill() {
	s.mu.Lock()
	pid := s.pid
	var proc *os.Process
	if s.cmd != nil {
		proc = s.cmd.Process
	}
	running := s.status == StatusRunning
	s.mu.Unlock()
	if !running {
		return
	}
	if pid > 0 && terminateProcessGroup(pid) {
		select {
		case <-s.exitedCh:
			return
		case <-time.After(terminationGracePeriod):
		}
	}
	if pid > 0 {
		killProcessGroup(pid)
	}
	if proc != nil {
		_ = proc.Kill()
	}
	<-s.exitedCh
}

// headTailBuffer retains a stable prefix (head) and the most recent suffix
// (tail) of everything written, dropping the middle once the budget is
// exceeded. The tail is compacted at 2x its share, so retained bytes stay
// between 1x and 1.5x the configured cap — the bound matters (a chatty session
// must not grow without limit), the exact number does not.
type headTailBuffer struct {
	head    []byte
	tail    []byte
	headMax int
	tailMax int
	total   int64
}

func newHeadTailBuffer(capBytes int) *headTailBuffer {
	head := capBytes / 2
	return &headTailBuffer{headMax: head, tailMax: capBytes - head}
}

// Write appends p, keeping the head and tail windows bounded.
func (b *headTailBuffer) Write(p []byte) {
	b.total += int64(len(p))
	if room := b.headMax - len(b.head); room > 0 {
		if len(p) <= room {
			b.head = append(b.head, p...)
			return
		}
		b.head = append(b.head, p[:room]...)
		p = p[room:]
	}
	b.tail = append(b.tail, p...)
	if b.tailMax > 0 && len(b.tail) > 2*b.tailMax {
		b.tail = append(b.tail[:0:0], b.tail[len(b.tail)-b.tailMax:]...)
	}
}

// written returns the absolute number of bytes ever written.
func (b *headTailBuffer) written() int64 { return b.total }

// readFrom returns the retained bytes at absolute offset from, the number of
// dropped bytes between the returned head and tail, and the next offset to
// resume from.
func (b *headTailBuffer) readFrom(from int64) (head, tail []byte, omitted int64, next int64) {
	next = b.total
	if from < 0 {
		from = 0
	}
	if from >= b.total {
		return nil, nil, 0, next
	}
	if from < int64(len(b.head)) {
		head = b.head[from:]
		from = int64(len(b.head))
	}
	droppedEnd := b.total - int64(len(b.tail))
	if from < droppedEnd {
		omitted = droppedEnd - from
		from = droppedEnd
	}
	if from < b.total {
		tail = b.tail[from-droppedEnd:]
	}
	return head, tail, omitted, next
}
