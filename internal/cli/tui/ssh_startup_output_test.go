package tui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

type startupInputProbe struct {
	Model
	key chan time.Time
}

type startupTestInput struct {
	*os.File
	eof chan struct{}
}

func (r startupTestInput) Read(p []byte) (int, error) {
	n, err := r.File.Read(p)
	if err == io.EOF {
		close(r.eof)
	}
	return n, err
}

func (m startupInputProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		select {
		case m.key <- time.Now():
		default:
		}
	}
	next, cmd := m.Model.Update(msg)
	m.Model = next.(Model)
	return m, cmd
}

func TestSSHStartupWritesNoAutomaticQueriesAndAcceptsFirstKey(t *testing.T) {
	clearMotionEnv(t)
	t.Setenv("SSH_TTY", "/dev/pts/test")
	t.Setenv("GOLDER_NO_TERMINAL_QUERIES", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	f, err := os.CreateTemp(t.TempDir(), "output-identity")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	out := &refreshOutput{}
	filtered := &queryOutput{File: f, sink: out}
	key := make(chan time.Time, 4)
	m := NewModel(Options{Model: "gpt-4.1", ProviderName: "openai"})
	input := startupTestInput{File: r, eof: make(chan struct{})}
	p := tea.NewProgram(startupInputProbe{Model: m, key: key}, tea.WithInput(input), tea.WithOutput(filtered),
		tea.WithWindowSize(211, 53), tea.WithColorProfile(colorprofile.ANSI), tea.WithoutSignalHandler(),
		tea.WithEnvironment([]string{"TERM=xterm", "SSH_TTY=/dev/pts/test"}))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	t.Cleanup(func() {
		// End the OS read before closing the borrowed file. StreamEvents may
		// return on cancellation before its read worker has left kqueue/epoll.
		_ = w.Close()
		select {
		case <-input.eof:
		case <-time.After(3 * time.Second):
			t.Error("startup input did not reach EOF")
		}
		p.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("startup probe: %v", err)
			}
		case <-time.After(3 * time.Second):
			p.Kill()
			t.Error("startup probe did not stop")
		}
		_ = filtered.Flush()
	})
	peek := func() string {
		out.mu.Lock()
		defer out.mu.Unlock()
		return out.buf.String()
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(peek(), "> ") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(peek(), "> ") {
		t.Fatal("query-free first frame did not arrive")
	}
	sent := time.Now()
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-key:
		t.Logf("local query-free first-frame-to-key latency=%s", received.Sub(sent))
	case <-time.After(time.Second):
		t.Fatal("first key waited for a terminal query reply that was never sent")
	}
	for _, query := range automaticTerminalQueries {
		if strings.Contains(peek(), string(query)) {
			t.Fatalf("SSH renderer emitted an automatic query %q", query)
		}
	}
}
