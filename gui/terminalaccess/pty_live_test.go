package terminalaccess

import (
	"bufio"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in because some sandboxes/CI runners cannot allocate a PTY. A denial is
// a capability answer; this test must not request a sandbox escape.
func TestProcessLivePTY(t *testing.T) {
	if os.Getenv("GOLDER_GUI_TERMINAL_LIVE_TEST") != "1" {
		t.Skip("set GOLDER_GUI_TERMINAL_LIVE_TEST=1 to exercise a real PTY")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX shell fixture is not available on Windows")
	}
	p, err := StartProcess(ProcessOptions{Argv: []string{"sh", "-c", `printf 'ready\n'; IFS= read -r line; printf 'got:%s\n' "$line"`}, Dir: t.TempDir()})
	if err != nil {
		t.Skipf("PTY unavailable in this environment: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Resize(96, 28); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		r := bufio.NewReader(p)
		for {
			line, err := r.ReadString('\n')
			if strings.Contains(line, "ready") {
				break
			}
			if err != nil {
				done <- err
				return
			}
		}
		if _, err := io.WriteString(p, "hello\n"); err != nil {
			done <- err
			return
		}
		for {
			line, err := r.ReadString('\n')
			if strings.Contains(line, "got:hello") {
				done <- nil
				return
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- errors.New("process exited without the expected response")
				} else {
					done <- err
				}
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PTY interaction timed out")
	}
}
