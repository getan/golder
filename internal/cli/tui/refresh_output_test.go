package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type refreshOutput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (o *refreshOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *refreshOutput) take() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.buf.String()
	o.buf.Reset()
	return s
}

type refreshBarrier chan struct{}

// Keep the real model and Bubble Tea renderer, but let the test inject clocks:
// discard commands rather than letting actual timers race a measured burst.
type refreshProbe struct{ Model }

func (m refreshProbe) Init() tea.Cmd { return nil }

func (m refreshProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if barrier, ok := msg.(refreshBarrier); ok {
		close(barrier)
		return m, nil
	}
	next, _ := m.Model.Update(msg)
	m.Model = next.(Model)
	return m, nil
}

// This checks actual terminal bytes, not just equality of two View strings.
// Without the fix each spaced token delta generated another hide/show pair.
func TestTokenAccountingDoesNotWriteTerminalFrames(t *testing.T) {
	m := NewModel(Options{NoAnimations: true})
	m.width, m.height, m.running = 80, 24, true
	start := time.Now()
	m.spinner.begin(start, "medium")
	m.relayout()
	out := &refreshOutput{}
	p := tea.NewProgram(refreshProbe{m}, tea.WithInput(nil), tea.WithOutput(out),
		tea.WithWindowSize(80, 24), tea.WithoutSignalHandler(),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "SSH_TTY=/dev/pts/test", "NO_COLOR=1"}))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	t.Cleanup(func() {
		p.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("renderer probe: %v", err)
			}
		case <-time.After(3 * time.Second):
			p.Kill()
			t.Error("renderer probe did not stop")
		}
	})
	barrier := func() {
		b := make(refreshBarrier)
		p.Send(b)
		select {
		case <-b:
		case <-time.After(3 * time.Second):
			t.Fatal("renderer probe did not process its input")
		}
	}
	barrier()
	time.Sleep(100 * time.Millisecond) // flush the startup frame/capability output
	out.take()
	for range 16 {
		p.Send(tokenDeltaMsg{chars: 8})
		time.Sleep(25 * time.Millisecond) // separate possible 60fps renderer flushes
	}
	barrier()
	time.Sleep(100 * time.Millisecond)
	if got := out.take(); got != "" {
		t.Fatalf("accounting-only messages wrote %d unnecessary terminal bytes", len(got))
	}
	p.Send(spinnerTickMsg(start.Add(time.Second)))
	barrier()
	time.Sleep(100 * time.Millisecond)
	got := out.take()
	if !strings.Contains(got, "32 tokens") {
		t.Fatalf("tick did not publish accumulated stats: %q", got)
	}
	if pairs := strings.Count(got, "\x1b[?25l"); pairs > 1 {
		t.Fatalf("one stats tick caused %d hide/show pairs", pairs)
	}
}
