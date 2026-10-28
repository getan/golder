//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
)

type standaloneCompletionProbe struct{ model tea.Model }

type standaloneInputReady struct{}

func (p standaloneCompletionProbe) Init() tea.Cmd {
	return tea.Batch(p.model.Init(), func() tea.Msg { return standaloneInputReady{} })
}
func (p standaloneCompletionProbe) View() tea.View { return p.model.View() }
func (p standaloneCompletionProbe) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(standaloneInputReady); ok {
		fmt.Fprint(os.Stderr, "\r\nSTANDALONE_INPUT_READY\r\n")
		return p, nil
	}
	model, cmd := p.model.Update(message)
	p.model = model
	if key, ok := message.(tea.KeyPressMsg); ok {
		if key.String() == "tab" {
			m := model.(Model)
			if m.quitting || m.running || m.input.Value() != "/provider " {
				panic(fmt.Sprintf("bad standalone completion: %q", m.input.Value()))
			}
			fmt.Fprint(os.Stderr, "\r\nSTANDALONE_TAB_COMPLETED\r\n")
		}
		if key.String() == "x" {
			fmt.Fprint(os.Stderr, "\r\nSTANDALONE_AFTER_TAB_ALIVE\r\n")
			return p, tea.Quit
		}
	}
	return p, cmd
}

func TestStandaloneCompletionPTYChild(t *testing.T) {
	if os.Getenv("GOLDER_TEST_STANDALONE_TAB") != "1" {
		return
	}
	model := NewModel(Options{NoAnimations: true, NoPromptTemplates: true})
	output := &queryOutput{File: os.Stdout, sink: os.Stdout}
	program := tea.NewProgram(standaloneCompletionProbe{model: model}, tea.WithOutput(output))
	_, err := program.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// No workspace daemon, mux, SSH server or remote account: the actual standalone
// Bubble Tea input decoder and renderer run on a PTY in an SSH-shaped environment.
func TestStandaloneProTabThroughRealPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStandaloneCompletionPTYChild$")
	cmd.Env = append(os.Environ(), "GOLDER_TEST_STANDALONE_TAB=1", "TERM=xterm-256color", "SSH_CONNECTION=192.0.2.1 50000 192.0.2.2 22", "SSH_TTY=/dev/pts/1")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Skipf("local PTY unavailable: %v", err)
	}
	defer terminal.Close()
	defer cmd.Process.Kill()
	output := make(chan string, 1)
	ready := make(chan struct{}, 1)
	completed := make(chan struct{}, 1)
	go func() {
		var text strings.Builder
		var buffer [4096]byte
		readySent, completedSent := false, false
		for text.Len() < 1<<20 {
			n, readErr := terminal.Read(buffer[:])
			text.Write(buffer[:n])
			if !readySent && strings.Contains(text.String(), "STANDALONE_INPUT_READY") {
				ready <- struct{}{}
				readySent = true
			}
			if !completedSent && strings.Contains(text.String(), "STANDALONE_TAB_COMPLETED") {
				completed <- struct{}{}
				completedSent = true
			}
			if readErr != nil {
				break
			}
		}
		output <- text.String()
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		_ = cmd.Wait()
		t.Fatalf("standalone program did not initialize: %s", <-output)
	}
	// Sending the raw bytes exercises batching as well as legacy ASCII HT.
	if _, err = terminal.Write([]byte("/pro\t")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-ctx.Done():
		_ = cmd.Wait()
		t.Fatalf("standalone program did not complete Tab: %s", <-output)
	}
	// Let the real renderer paint the post-completion frame before checking
	// another ordinary input event; the fixture's x ends the test deliberately.
	time.Sleep(50 * time.Millisecond)
	if _, err = terminal.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	text := <-output
	if err != nil || !strings.Contains(text, "STANDALONE_TAB_COMPLETED") || !strings.Contains(text, "STANDALONE_AFTER_TAB_ALIVE") {
		t.Fatalf("standalone Tab exited unexpectedly: %v\n%s", err, text)
	}
}
