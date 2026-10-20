package tui

// Opt-in latency diagnostics. TEA_TRACE logs the raw terminal stream, including
// prompts and keystrokes; this trace records only byte counts, message TYPES,
// terminal capabilities and timing. It can safely distinguish a late input read
// from a blocked Update/View or a slow terminal write without recording what the
// person typed. Disabled by default; use a local file (e.g. /tmp), not NFS.

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

type timingTrace struct {
	log   *log.Logger
	file  io.Closer
	start time.Time
}

func openTimingTrace(path string) (*timingTrace, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	t := &timingTrace{
		log:  log.New(f, fmt.Sprintf("pid=%d ", os.Getpid()), log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC),
		file: f, start: time.Now(),
	}
	t.event("start os=%s arch=%s go=%s", runtime.GOOS, runtime.GOARCH, runtime.Version())
	// Deliberately not os.Environ(): it contains credentials. These names only
	// describe the terminal. Presence (not values) is enough for host variables.
	for _, key := range []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "CLICOLOR", "CLICOLOR_FORCE"} {
		t.event("env %s=%q", key, os.Getenv(key))
	}
	for _, key := range []string{"NO_COLOR", "TMUX", "STY", "SSH_TTY", "SSH_CONNECTION", "WT_SESSION"} {
		_, set := os.LookupEnv(key)
		t.event("env %s_set=%t", key, set)
	}
	return t, nil
}

func (t *timingTrace) event(format string, args ...any) {
	if t != nil {
		t.log.Printf("+%.3fms %s", float64(time.Since(t.start))/float64(time.Millisecond), fmt.Sprintf(format, args...))
	}
}

func (t *timingTrace) close() {
	if t != nil {
		t.event("trace end")
		_ = t.file.Close()
	}
}

func (t *timingTrace) program(m tea.Model) (tea.Model, []tea.ProgramOption) {
	if t == nil {
		return m, nil
	}
	inputTTY := term.IsTerminal(os.Stdin.Fd())
	t.event("io stdin_tty=%t stdout_tty=%t input_wrapped=%t", inputTTY, term.IsTerminal(os.Stdout.Fd()), inputTTY)
	opts := []tea.ProgramOption{tea.WithOutput(timingOutput{File: os.Stdout, trace: t})}
	// Preserve Bubble Tea's /dev/tty fallback when stdin is redirected. The
	// wrapper embeds the file so Fd/Name still reach the raw-mode and epoll
	// paths; replacing it with an io.Reader-only wrapper changes that behavior.
	if inputTTY {
		opts = append(opts, tea.WithInput(timingInput{File: os.Stdin, trace: t}))
	}
	return timingModel{Model: m, trace: t}, opts
}

type timingInput struct {
	*os.File
	trace *timingTrace
}

func (r timingInput) Read(p []byte) (int, error) {
	r.trace.event("input begin")
	start := time.Now()
	n, err := r.File.Read(p)
	r.trace.event("input read bytes=%d duration=%s failed=%t", n, time.Since(start), err != nil)
	return n, err
}

type timingOutput struct {
	*os.File
	trace *timingTrace
}

func (w timingOutput) Write(p []byte) (int, error) {
	w.trace.event("output begin bytes=%d", len(p))
	start := time.Now()
	n, err := w.File.Write(p)
	w.trace.event("output end bytes=%d duration=%s failed=%t", n, time.Since(start), err != nil)
	return n, err
}

type timingModel struct {
	tea.Model
	trace *timingTrace
}

func (m timingModel) Init() tea.Cmd {
	m.trace.event("init begin")
	start := time.Now()
	cmd := m.Model.Init()
	m.trace.event("init end duration=%s", time.Since(start))
	return cmd
}

func (m timingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// No msg.String() or %+v here: a KeyPressMsg/PasteMsg contains user input.
	m.trace.event("update begin type=%T", msg)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.trace.event("window width=%d height=%d", msg.Width, msg.Height)
	case tea.ColorProfileMsg:
		m.trace.event("color profile=%s", msg.Profile)
	case tea.ModeReportMsg:
		m.trace.event("mode number=%v value=%v", msg.Mode, msg.Value)
	case tea.KeyboardEnhancementsMsg:
		m.trace.event("keyboard flags=%d", msg.Flags)
	}
	start := time.Now()
	next, cmd := m.Model.Update(msg)
	m.trace.event("update end type=%T duration=%s", msg, time.Since(start))
	m.Model = next
	return m, cmd
}

func (m timingModel) View() tea.View {
	m.trace.event("view begin")
	start := time.Now()
	v := m.Model.View()
	m.trace.event("view end bytes=%d duration=%s", len(v.Content), time.Since(start))
	return v
}
