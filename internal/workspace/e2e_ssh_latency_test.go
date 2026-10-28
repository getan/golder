//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini/control"
)

// The simulated SSH terminal intentionally answers no capability queries.
// A missing reply must not make a fresh or reattached workspace wait out a
// daemon/terminal timeout before accepting the first ordinary key.
func TestE2ESSHWorkspaceFirstInputDoesNotWaitForTerminalReplies(t *testing.T) {
	bin := golderBinary(t)
	for _, name := range []string{"default", "ssh-other"} {
		for _, agent := range []bool{false, true} {
			kind := "shell"
			if agent {
				kind = "agent"
			}
			t.Run(name+"/"+kind, func(t *testing.T) {
				root := tempRoot(t)
				dir := project(t, root)
				socket := filepath.Join(root, "s.sock")
				env := append(isolatedEnv(t, root), "SSH_TTY=/dev/pts/fixture", "SSH_CONNECTION=192.0.2.1 1234 192.0.2.2 22")
				t.Cleanup(func() { stopQuietly(bin, dir, env, "-s", name) })
				args := []string{"workspace", "-s", name, "--socket", socket}
				if agent {
					args = append(args, "--", "--approve", "--no-skills", "--no-prompt-templates", "--provider", "openai", "--model", "gpt-4.1", "--api-key", "fake-test-key")
				} else {
					args = append(args, "--shell")
				}
				for pass := range 2 {
					if pass == 1 {
						args = []string{"workspace", "attach", name}
					}
					started := time.Now()
					v := newTerminal(t).start(t, bin, dir, env, args...)
					v.waitForOutput(t, "\x1b[?1049h", 8*time.Second, "SSH workspace's initial frame")
					if agent {
						prompt := "Type a message"
						if pass == 1 {
							prompt = "ssh-first-key-0" // the preserved draft replaces the placeholder
						}
						v.waitForOutput(t, prompt, 8*time.Second, "SSH agent composer")
					}
					ready := time.Now()
					if agent {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						ctl, err := control.Dial(ctx, socket)
						if err != nil {
							cancel()
							t.Fatal(err)
						}
						marker := fmt.Sprintf("ssh-first-key-%d", pass)
						v.typeKeys(t, "\x15"+marker)
						waitFor(t, 2*time.Second, "first SSH key reaching agent", func() bool {
							raw, err := ctl.Call(ctx, "capture-pane", map[string]any{"session": name, "source": "visible", "lines": 70})
							var captured struct{ Content string }
							return err == nil && json.Unmarshal(raw, &captured) == nil && strings.Contains(captured.Content, marker)
						})
						_ = ctl.Close()
						cancel()
					} else {
						marker := filepath.Join(dir, "first-key")
						_ = os.Remove(marker)
						v.typeKeys(t, "printf 'first-key-arrived' > first-key\r")
						waitFor(t, 2*time.Second, "first SSH key reaching shell", func() bool {
							body, err := os.ReadFile(marker)
							return err == nil && string(body) == "first-key-arrived"
						})
					}
					t.Logf("pass=%d first_frame_and_prompt=%s first_input=%s", pass, ready.Sub(started), time.Since(ready))
					v.typeKeys(t, "\x02d")
					if err := v.waitExit(t, 8*time.Second); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
