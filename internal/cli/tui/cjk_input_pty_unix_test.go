//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
)

// cjkInputProbe reports what the real Bubble Tea decoder delivered to the
// composer, then quits on "x".
type cjkInputProbe struct {
	model tea.Model
	done  bool
}

type cjkInputReady struct{}

func (p cjkInputProbe) Init() tea.Cmd {
	return tea.Batch(p.model.Init(), func() tea.Msg { return cjkInputReady{} })
}

func (p cjkInputProbe) View() tea.View { return p.model.View() }

func (p cjkInputProbe) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(cjkInputReady); ok {
		fmt.Fprint(os.Stderr, "\r\nCJK_INPUT_READY\r\n")
		return p, nil
	}
	model, cmd := p.model.Update(message)
	p.model = model
	key, ok := message.(tea.KeyPressMsg)
	if !ok || key.String() != "x" || p.done {
		return p, cmd
	}
	p.done = true
	fmt.Fprintf(os.Stderr, "\r\nCJK_INPUT_VALUE=%q\r\n", model.(Model).input.Value())
	return p, tea.Quit
}

func TestCJKInputPTYChild(t *testing.T) {
	if os.Getenv("GOLDER_TEST_CJK_INPUT") != "1" {
		return
	}
	model := NewModel(Options{NoAnimations: true, NoPromptTemplates: true})
	output := &queryOutput{File: os.Stdout, sink: os.Stdout}
	program := tea.NewProgram(cjkInputProbe{model: model}, tea.WithOutput(output))
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// A terminal may deliver "ESC [" in one read and the following keystrokes in
// the next. Every byte after the introducer used to be held as frame payload
// until an ASCII final byte arrived, so CJK typed through an IME never reached
// the composer. This runs the real decoder on a PTY: the introducer and the
// CJK arrive in separate writes, well inside the Esc ambiguity window.
func TestCJKInputAfterSplitCSIIntroducerThroughRealPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCJKInputPTYChild$")
	cmd.Env = append(os.Environ(), "GOLDER_TEST_CJK_INPUT=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Skipf("local PTY unavailable: %v", err)
	}
	defer terminal.Close()
	defer cmd.Process.Kill()

	readyCh := make(chan struct{}, 1)
	var collected strings.Builder
	var collectedMu sync.Mutex
	snapshot := func() string {
		collectedMu.Lock()
		defer collectedMu.Unlock()
		return collected.String()
	}
	go func() {
		var buffer [4096]byte
		for {
			n, readErr := terminal.Read(buffer[:])
			collectedMu.Lock()
			collected.Write(buffer[:n])
			ready := strings.Contains(collected.String(), "CJK_INPUT_READY")
			collectedMu.Unlock()
			if ready {
				select {
				case readyCh <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Wait for the program to take the terminal before typing.
	select {
	case <-readyCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("standalone program did not initialize: %s", snapshot())
	}

	if _, err := terminal.Write([]byte("\x1b[")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // the frame is still pending
	if _, err := terminal.Write([]byte("中文")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := terminal.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for !strings.Contains(snapshot(), "CJK_INPUT_VALUE=") {
		select {
		case <-ctx.Done():
			t.Fatalf("program did not quit; CJK input was swallowed: %s", snapshot())
		case <-deadline:
			t.Fatalf("composer never received the CJK text: %s", snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The probe quits on "x" after the decoder inserted it, so the composer ends
	// with "中文x"; the CJK prefix is what this regression is about.
	if text := snapshot(); !strings.Contains(text, `CJK_INPUT_VALUE="中文`) {
		t.Fatalf("composer did not receive the CJK text:\n%s", text)
	}
}
