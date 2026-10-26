package tui

// Opt-in latency diagnostics. TEA_TRACE logs the raw terminal stream, including
// prompts and keystrokes; this trace records only byte counts, message TYPES and
// timing. It can safely distinguish a late input read from a blocked Update/View
// or a slow terminal write without recording what the person typed. The file,
// clock, switch and privacy rules live in internal/diagtrace, shared with main,
// run assembly, plugins and hooks so startup and TUI share one timeline.

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/getan/golder/internal/diagtrace"
)

type timingTrace struct {
	trace *diagtrace.Trace
}

// newTimingTrace adapts the process trace for the TUI. The returned value is
// nil when diagnostics are disabled; every method is nil-safe.
func newTimingTrace(tr *diagtrace.Trace) *timingTrace {
	if tr == nil {
		return nil
	}
	return &timingTrace{trace: tr}
}

func (t *timingTrace) event(format string, args ...any) {
	if t != nil {
		t.trace.Event("tui", format, args...)
	}
}

func (t *timingTrace) close() {
	if t != nil {
		// The process trace owns the file and writes "diag trace end" on exit.
		t.event("tui end")
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
