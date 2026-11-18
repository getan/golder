package androidagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func runForTest(t *testing.T, mode permissions.Mode) (*runTool, string) {
	t.Helper()
	root := t.TempDir()
	bin := t.TempDir()
	return &runTool{root: root, binDir: bin, perms: permissions.New(mode)}, root
}

func execRun(t *testing.T, tool *runTool, argv ...string) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"argv": argv})
	result, err := tool.Execute(context.Background(), "test", raw, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return textOfTool(result), result.IsError
}

func textOfTool(result agentcore.AgentToolResult) string {
	return agentcore.ContentToText(result.Content)
}

func TestRunRejectsShellAndUnknownCommands(t *testing.T) {
	tool, _ := runForTest(t, permissions.Auto)
	for _, argv := range [][]string{
		{"sh", "-c", "echo hi"},
		{"bash", "-c", "echo hi"},
		{"xargs", "rm"},
	} {
		text, isErr := execRun(t, tool, argv...)
		if !isErr || !strings.Contains(text, "not in the Android allowlist") {
			t.Errorf("%v = %q, error=%v; want allowlist rejection", argv, text, isErr)
		}
	}
}

func TestRunRejectsEscapingPaths(t *testing.T) {
	tool, _ := runForTest(t, permissions.Auto)
	for _, argv := range [][]string{
		{"cat", "/etc/passwd"},
		{"cat", "../secret"},
		{"rm", "-rf", ".."},
	} {
		text, isErr := execRun(t, tool, argv...)
		if !isErr || !strings.Contains(text, "workspace") {
			t.Errorf("%v = %q, error=%v; want a workspace rejection", argv, text, isErr)
		}
	}
}

func TestRunRejectsDangerousFlags(t *testing.T) {
	tool, _ := runForTest(t, permissions.Auto)
	for _, argv := range [][]string{
		{"find", ".", "-exec", "rm", "{}", ";"},
		{"sed", "-i", "s/a/b/", "file"},
		{"tar", "-xf", "archive.tar"},
		{"sort", "-o", "out", "file"},
	} {
		text, isErr := execRun(t, tool, argv...)
		if !isErr {
			t.Errorf("%v = %q, want rejection", argv, text)
		}
	}
}

func TestRunBlocksWritesInReadOnlyMode(t *testing.T) {
	tool, _ := runForTest(t, permissions.ReadOnly)
	text, isErr := execRun(t, tool, "rm", "file")
	if !isErr || !strings.Contains(text, "read-only") {
		t.Fatalf("rm in read-only = %q, error=%v", text, isErr)
	}
}

func TestRunExecutesAllowlistedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/ls")
	}
	tool, root := runForTest(t, permissions.Auto)
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/ls", filepath.Join(tool.binDir, "ls")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	text, isErr := execRun(t, tool, "ls")
	if isErr || !strings.Contains(text, "hello.txt") {
		t.Fatalf("ls = %q, error=%v; want hello.txt", text, isErr)
	}
}
