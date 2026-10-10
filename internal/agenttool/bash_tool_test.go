package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func runBash(t *testing.T, tool *BashTool, args map[string]any, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), "call-1", raw, onUpdate)
}

func TestBashToolSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo hello"}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	if !strings.Contains(resultText(res), "hello") {
		t.Errorf("output = %q, want to contain hello", resultText(res))
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["exitCode"] != 0 {
		t.Errorf("expected exitCode 0, details = %+v", res.Details)
	}
}

func TestBashToolNonZeroExitIsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo oops >&2; exit 3"}, nil)
	// A non-zero exit must surface as a Go error so the executor flags isError.
	if gerr == nil {
		t.Fatalf("expected go error for non-zero exit, got nil")
	}
	if !strings.Contains(gerr.Error(), "code 3") {
		t.Errorf("error = %q, want to mention code 3", gerr.Error())
	}
	// The captured output must ride along.
	if !strings.Contains(gerr.Error(), "oops") {
		t.Errorf("error = %q, want to carry output", gerr.Error())
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["exitCode"] != 3 {
		t.Errorf("expected exitCode 3, details = %+v", res.Details)
	}
}

func TestBashToolStreaming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	var mu sync.Mutex
	var updates []string
	onUpdate := func(r agentcore.AgentToolResult) {
		mu.Lock()
		updates = append(updates, resultText(r))
		mu.Unlock()
	}
	_, gerr := runBash(t, tool, map[string]any{"command": "printf 'a'; printf 'b'"}, onUpdate)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatalf("expected streaming updates, got none")
	}
	// Updates are incremental deltas: concatenating them in order must
	// reproduce the full accumulated output.
	if got := strings.Join(updates, ""); !strings.Contains(got, "ab") {
		t.Errorf("joined updates = %q, want to contain ab", got)
	}
}

func TestBashToolTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	start := time.Now()
	res, gerr := runBash(t, tool, map[string]any{"command": "sleep 5", "timeout_ms": 100}, nil)
	if gerr == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(gerr.Error(), "timed out") {
		t.Errorf("error = %q, want to mention timed out", gerr.Error())
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took too long: %s (process not killed?)", elapsed)
	}
	_ = res
}

func TestBashToolCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	ctx, cancel := context.WithCancel(context.Background())
	raw, _ := json.Marshal(map[string]any{"command": "sleep 5"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, gerr := tool.Execute(ctx, "call-1", raw, nil)
	if gerr == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancel took too long: %s (process not killed?)", elapsed)
	}
}

func TestBashToolMissingCommand(t *testing.T) {
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": ""}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	if !strings.Contains(resultText(res), "command is required") {
		t.Errorf("expected command-required error, got %q", resultText(res))
	}
}

func TestBashToolMode(t *testing.T) {
	tool := &BashTool{}
	if tool.Name() != "bash" {
		t.Errorf("name = %q", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionSequential {
		t.Error("bash should be sequential")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}

func TestBashToolSmallOutputNotTruncated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	res, gerr := runBash(t, tool, map[string]any{"command": "echo hello world"}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	out := resultText(res)
	if strings.Contains(out, "truncated") {
		t.Errorf("small output should not be truncated, got %q", out)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Errorf("output = %q, want %q", out, "hello world")
	}
}

func TestBashToolLargeOutputTruncatedHeadTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	tool := &BashTool{}
	// Emit a marker at the very start and very end, with a large filler between,
	// so we can prove both the head and the tail survive truncation.
	total := bashMaxOutputBytes * 3
	filler := bashMaxOutputBytes // bytes of 'x' between the two markers
	cmd := fmt.Sprintf("printf 'HEADMARK'; head -c %d /dev/zero | tr '\\0' 'x'; printf 'TAILMARK'", filler)
	_ = total
	res, gerr := runBash(t, tool, map[string]any{"command": cmd}, nil)
	if gerr != nil {
		t.Fatalf("unexpected go error: %v", gerr)
	}
	out := resultText(res)
	if len(out) > bashMaxOutputBytes+128 {
		t.Errorf("truncated output too long: %d bytes (cap %d)", len(out), bashMaxOutputBytes)
	}
	if !strings.HasPrefix(out, "HEADMARK") {
		t.Errorf("head not preserved; output starts with %q", out[:min(16, len(out))])
	}
	if !strings.HasSuffix(out, "TAILMARK") {
		t.Errorf("tail not preserved; output ends with %q", out[max(0, len(out)-16):])
	}
	if !strings.Contains(out, "[truncated ") || !strings.Contains(out, " bytes]") {
		t.Errorf("missing truncation marker in %q", out)
	}
}

func TestTruncateBashOutputByteCount(t *testing.T) {
	// A pure-ASCII input of a known size: the marker's N must equal the exact
	// number of middle bytes dropped, i.e. total - head - tail.
	total := bashMaxOutputBytes * 2
	in := strings.Repeat("z", total)
	out := truncateBashOutput(in)

	half := bashMaxOutputBytes / 2
	// For all-ASCII input no rune-boundary trimming happens, so head/tail are
	// each exactly half and N = total - 2*half.
	wantRemoved := total - 2*half
	wantMarker := fmt.Sprintf("[truncated %d bytes]", wantRemoved)
	if !strings.Contains(out, wantMarker) {
		t.Errorf("marker = ...%q..., want to contain %q", out, wantMarker)
	}
	if got := strings.Count(out, "z"); got != 2*half {
		t.Errorf("preserved %d content bytes, want %d (head+tail)", got, 2*half)
	}

	// Input at or below the cap is returned verbatim.
	small := strings.Repeat("b", bashMaxOutputBytes)
	if got := truncateBashOutput(small); got != small {
		t.Errorf("input at cap should be unchanged")
	}
}

// TestResolveShell covers the platform-aware interpreter selection (issue #518).
// It injects goos + a lookPath stub so every branch runs regardless of the host.
func TestResolveShell(t *testing.T) {
	found := func(name string) func(string) (string, error) {
		return func(s string) (string, error) {
			if s == name {
				return `C:\bin\` + s, nil
			}
			return "", fmt.Errorf("not found")
		}
	}
	none := func(string) (string, error) { return "", fmt.Errorf("not found") }

	tests := []struct {
		name           string
		explicit, goos string
		lookPath       func(string) (string, error)
		wantFlag       string
		wantShellHas   string // substring the resolved shell must contain
	}{
		{"explicit honored on windows", "zsh", "windows", none, "-c", "zsh"},
		{"explicit honored on linux", "fish", "linux", none, "-c", "fish"},
		{"non-windows always bash", "", "linux", none, "-c", "bash"},
		{"darwin always bash", "", "darwin", none, "-c", "bash"},
		{"windows with bash", "", "windows", found("bash"), "-c", "bash"},
		{"windows falls back to powershell", "", "windows", found("powershell"), "-Command", "powershell"},
		{"windows falls back to cmd", "", "windows", none, "/C", "cmd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shell, flag := resolveShell(tc.explicit, tc.goos, tc.lookPath)
			if flag != tc.wantFlag {
				t.Errorf("flag = %q, want %q", flag, tc.wantFlag)
			}
			if !strings.Contains(shell, tc.wantShellHas) {
				t.Errorf("shell = %q, want to contain %q", shell, tc.wantShellHas)
			}
		})
	}
}

// TestLooksRGMissing covers the measured shell shapes and, just as important,
// the outputs that must NOT be read as "ripgrep is missing": a bare "command
// not found" for some other program, and a grep that failed on a file named
// `rg`. A false positive would tell the model to stop using the tools when
// nothing is wrong with them.
func TestLooksRGMissing(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"bash", "/bin/bash: rg: command not found\n", true},
		{"zsh", "zsh:1: command not found: rg\n", true},
		{"dash (measured)", "/bin/dash: 1: rg: not found\n", true},
		{"debian /bin/sh is dash", "sh: 1: rg: not found\n", true},
		{"busybox ash", "sh: rg: not found\n", true},
		{"path unset", "rg: No such file or directory\n", true},
		{"indented", "  /bin/bash: rg: command not found\n", true},
		{"other program missing", "/bin/bash: nosuchcmd: command not found\n", false},
		{"grep on a file named rg", "grep: rg: No such file or directory\n", false},
		{"rg mentioned mid-line", "plugin rg: not found in registry\n", false},
		{"ordinary failure", "make: *** [all] Error 1\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksRGMissing(c.output); got != c.want {
				t.Errorf("looksRGMissing(%q) = %v, want %v", c.output, got, c.want)
			}
		})
	}
}

// TestBashRGMissingHint is the integration: a command that fails because rg is
// not on PATH comes back with the fallback hint, so the model learns to use the
// shell's grep/find instead of trying the ripgrep-backed tools next. PATH is
// overridden inline rather than by mutating the test process's environment, so
// the assertion does not depend on this machine having ripgrep.
func TestBashRGMissingHint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	res, gerr := runBash(t, &BashTool{}, map[string]any{
		"command": "PATH=/nonexistent-dir-for-test rg --version",
	}, nil)
	if gerr == nil {
		t.Fatalf("a missing rg must fail the command, got nil error")
	}
	if !strings.Contains(gerr.Error(), "grep/find") {
		t.Errorf("error should carry the fallback hint, got %q", gerr.Error())
	}
	if !strings.Contains(gerr.Error(), "not installed") {
		t.Errorf("error should say ripgrep is not installed, got %q", gerr.Error())
	}
	details, ok := res.Details.(map[string]any)
	if !ok || details["rgMissing"] != true {
		t.Errorf("expected rgMissing in details, got %+v", res.Details)
	}

	// An ordinary failure must not carry the ripgrep advice.
	res, gerr = runBash(t, &BashTool{}, map[string]any{"command": "exit 4"}, nil)
	if gerr == nil {
		t.Fatalf("expected a failure for exit 4")
	}
	if strings.Contains(gerr.Error(), "not installed") {
		t.Errorf("an unrelated failure must not mention ripgrep, got %q", gerr.Error())
	}
	details, ok = res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details missing: %+v", res.Details)
	}
	if _, present := details["rgMissing"]; present {
		t.Errorf("rgMissing must be absent for an unrelated failure: %+v", details)
	}
}
