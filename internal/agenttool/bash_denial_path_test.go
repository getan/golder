package agenttool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// denialEchoRunner is a sandbox runner whose command always fails with one
// denial line, whatever the caller asked it to run.
type denialEchoRunner struct{ line string }

func (r *denialEchoRunner) SandboxArgv(shell, flag, _, _ string) ([]string, func(), error) {
	return []string{shell, flag, "echo '" + r.line + "' >&2; exit 1"}, func() {}, nil
}

func (r *denialEchoRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	return []string{argv[0], argv[1], "echo '" + r.line + "' >&2; exit 1"}, func() {}, nil
}

// TestBashSandboxDenialPrefersThePathTheOSNamed: the denial message is ground
// truth. When the command names one path and the sandbox refused another — a
// variable, a loop, a generated name — the question must be about the refused
// one, because that is the path the retry needs.
func TestBashSandboxDenialPrefersThePathTheOSNamed(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	refused := filepath.Join(home, "probe-denied", "out")
	named := filepath.Join(home, "elsewhere", "out")
	runner := &denialEchoRunner{line: "cp: cannot create regular file '" + refused + "': Operation not permitted"}

	var asked []string
	tool := &BashTool{
		Dir: t.TempDir(), Sandbox: runner, ForceSandbox: true,
		Permissions: permissions.New(permissions.Auto),
		OnSandboxDenied: func(_ context.Context, path, _ string) SandboxDenialAnswer {
			asked = append(asked, path)
			return SandboxDenialRefused
		},
	}
	if _, err := runBashErr(t, tool, map[string]any{"command": "cp seed " + named}); err == nil {
		t.Fatal("expected the sandboxed probe to fail")
	}
	if len(asked) != 1 || asked[0] != refused {
		t.Fatalf("asked = %v, want the path the sandbox named (%s)", asked, refused)
	}
}
