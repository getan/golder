//go:build darwin || linux

package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

// Exercise the outer UI only: no agent is started, the shown workspace has
// no panes, and the test answers no terminal capability queries.
func TestE2ESSHWorkspaceWelcomeAcceptsOuterUIKeys(t *testing.T) {
	bin := golderBinary(t)
	for _, terminalType := range []string{"xterm-256color", "linux"} {
		t.Run(terminalType, func(t *testing.T) {
			root := tempRoot(t)
			dir := project(t, root)
			env := append(isolatedEnv(t, root), "SSH_TTY=/dev/pts/fixture", "SSH_CONNECTION=192.0.2.1 1234 192.0.2.2 22")
			// Replace, rather than append, TERM: duplicate names may resolve
			// differently through exec and must not invalidate the 16-color case.
			for i, entry := range env {
				if len(entry) >= 5 && entry[:5] == "TERM=" {
					env[i] = "TERM=" + terminalType
				}
			}
			t.Cleanup(func() { stopQuietly(bin, dir, env, "-s", "welcome") })
			v := newTerminal(t).start(t, bin, dir, env, "workspace", "--shell", "-s", "welcome", "--socket", filepath.Join(root, "s.sock"))
			v.waitForOutput(t, "\x1b[?1049h", 8*time.Second, "outer workspace frame")
			// The shell remains on workspace 1; workspace 2 shows the actual
			// GOLDER/AGENT WORKSPACE welcome screen, not an inner golder TUI.
			v.typeKeys(t, "\x1b2")
			v.waitForOutput(t, "AGENT WORKSPACE", 3*time.Second, "empty workspace welcome")
			sent := time.Now()
			v.typeKeys(t, "?")
			v.waitForOutput(t, "Keybindings", 2*time.Second, "outer help overlay")
			t.Logf("outer welcome-to-help latency=%s", time.Since(sent))
			v.typeKeys(t, "\x1b")
			time.Sleep(100 * time.Millisecond) // distinguish Escape from the following leader
			v.typeKeys(t, "\x02d")
			if err := v.waitExit(t, 8*time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}
