//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini/control"
)

// There is no golder TUI in this pane. If mouse bytes reach this shell's read,
// the outer workspace decoder has already corrupted them into ordinary keys.
func TestE2EWorkspaceDoesNotTypeSplitMouseReportsIntoShell(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	t.Cleanup(func() { stopQuietly(bin, dir, env, "--session", "mouseinput") })
	args := append([]string{"workspace", "--shell", "--no-animations"}, workspaceArgs(dir, socket, "mouseinput")...)
	v := newTerminal(t).start(t, bin, dir, env, args...)
	v.waitForOutput(t, "\x1b[?2026h", 60*time.Second, "workspace frame")
	v.typeKeys(t, "echo reader-$((1+1))-ready; IFS= read -r line; printf '%s' \"$line\" > mouse-input.txt\r")
	v.waitForOutput(t, "reader-2-ready", 30*time.Second, "shell reader ready")
	v.typeKeys(t, "\x1b[<0;")
	time.Sleep(150 * time.Millisecond) // well beyond the old 50ms Esc timeout
	v.typeKeys(t, "65;59Mmarker\r")
	var got []byte
	waitFor(t, 10*time.Second, "shell input record", func() bool {
		var err error
		got, err = os.ReadFile(filepath.Join(dir, "mouse-input.txt"))
		return err == nil && len(got) > 0
	})
	if string(got) != "marker" {
		t.Fatalf("outer workspace typed a mouse-report tail into the shell: %q", got)
	}
}

func TestE2EWorkspaceReportsDoNotLeakIntoAgentComposer(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	t.Cleanup(func() { stopQuietly(bin, dir, env, "--session", "agentinput") })
	args := append([]string{"workspace", "--no-animations"}, workspaceArgs(dir, socket, "agentinput")...)
	// No Enter is sent to the agent, and the key is a fake fixture. This test
	// exercises real terminal I/O without making any provider/network request.
	args = append(args, "--", "--approve", "--no-animations", "--no-skills", "--no-prompt-templates", "--provider", "openai", "--model", "gpt-4.1", "--api-key", "fake-test-key")
	v := newTerminal(t).start(t, bin, dir, env, args...)
	v.waitForOutput(t, "Type a message", 30*time.Second, "inner agent composer")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctl, err := control.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	composer := func() string {
		raw, err := ctl.Call(ctx, "capture-pane", map[string]any{"session": "agentinput", "source": "visible", "lines": 70})
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Content string }
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out.Content, "\n") {
			if strings.HasPrefix(line, "> ") {
				return strings.TrimRight(strings.TrimPrefix(line, "> "), " ")
			}
		}
		return ""
	}
	for _, tc := range []struct{ name, first, rest string }{
		{"mouse", "\x1b[<0;", "65;59M"},
		{"mouse release", "\x1b[<0;65;", "59m"},
		{"background color", "\x1b]11;", "rgb:0000/0000/0000\x07"},
		{"split string terminator", "\x1b]10;rgb:ffff/ffff/ffff\x1b", "\\"},
		{"cursor position", "\x1b[?59;", "65R"},
		{"attributes", "\x1b[?1;", "2c"},
		{"focus", "\x1b[", "I"},
		{"graphics reply", "\x1b_Gi=1;", "OK\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v.typeKeys(t, "\x15"+tc.first) // Ctrl+U clears the previous draft
			time.Sleep(150 * time.Millisecond)
			v.typeKeys(t, tc.rest+"marker")
			waitFor(t, 5*time.Second, "agent marker", func() bool { return strings.Contains(composer(), "marker") })
			if got := composer(); got != "marker" {
				t.Fatalf("terminal report entered the real agent composer: %q", got)
			}
		})
	}
	// Text that only resembles a report must never be deleted by a heuristic.
	v.typeKeys(t, "\x1565;59M")
	waitFor(t, 5*time.Second, "literal report-looking text", func() bool { return composer() == "65;59M" })
}
