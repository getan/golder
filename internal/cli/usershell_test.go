package cli

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/execsess"
)

// requireUserShell skips the passthrough tests on Windows: userShellArgv
// prefers a POSIX login shell, and the tests below drive it with POSIX commands.
func requireUserShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the passthrough uses a POSIX login shell")
	}
}

func TestUserShellRecordRoundTrip(t *testing.T) {
	text := UserShellRecord("ls -la", 0, 1500*time.Millisecond, "file1\nfile2\n")
	d, ok := ParseUserShellRecord(text)
	if !ok {
		t.Fatalf("ParseUserShellRecord(%q) = false, want true", text)
	}
	if d.Command != "ls -la" {
		t.Errorf("Command = %q, want %q", d.Command, "ls -la")
	}
	if d.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", d.ExitCode)
	}
	if d.Output != "file1\nfile2\n" {
		t.Errorf("Output = %q, want %q", d.Output, "file1\nfile2\n")
	}

	// A failing command's negative/positive exit code survives the round trip.
	fail := UserShellRecord("false", 1, 20*time.Millisecond, "")
	fd, ok := ParseUserShellRecord(fail)
	if !ok || fd.ExitCode != 1 {
		t.Fatalf("failed record = %+v, ok=%v; want exit 1", fd, ok)
	}
}

func TestParseUserShellRecordRejectsOtherText(t *testing.T) {
	for _, text := range []string{
		"",
		"hello",
		"<user_shell_command>",
		"<user_shell_command>\n<command>\nls\n</command>\nno result\n</user_shell_command>",
	} {
		if _, ok := ParseUserShellRecord(text); ok {
			t.Errorf("ParseUserShellRecord(%q) = true, want false", text)
		}
	}
}

func TestUserShellMessageCapsContextOutput(t *testing.T) {
	head := strings.Repeat("H", 40_000)
	tail := strings.Repeat("T", 40_000)
	msg := UserShellMessage("chatty", 0, time.Second, head+tail)
	text := agentcore.ContentToText(msg.Content)
	// codex caps the output with its truncation policy and only then wraps it
	// in the command envelope, so the envelope's fixed overhead sits outside
	// the budget. Mirror that shape: budget + truncation marker + envelope.
	limit := userShellContextBudget +
		len("\n[truncated 50000 bytes]\n") +
		len(UserShellRecord("chatty", 0, time.Second, ""))
	if len(text) > limit {
		t.Fatalf("recorded message is %d bytes, want it capped near %d", len(text), limit)
	}
	if !strings.Contains(text, head[:1000]) {
		t.Errorf("recorded message lost its head preview")
	}
	if !strings.Contains(text, "[truncated ") {
		t.Errorf("recorded message lacks the truncation marker")
	}
	if !strings.Contains(text, tail[len(tail)-1000:]) {
		t.Errorf("recorded message lost its tail preview")
	}
	// The parsed form still recovers the command and the capped output.
	d, ok := ParseUserShellRecord(text)
	if !ok || d.Command != "chatty" {
		t.Fatalf("ParseUserShellRecord round trip failed: %+v ok=%v", d, ok)
	}
}

func TestTruncateUserShellOutputKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("中文测试", 10_000)
	out := truncateUserShellOutput(s, 101)
	if !utf8.ValidString(out) {
		t.Fatalf("truncated output is not valid UTF-8")
	}
	if !strings.Contains(out, "[truncated ") {
		t.Fatalf("truncated output lacks the marker: %q", out)
	}
	if len(out) > 101+len("[truncated 159899 bytes]")+8 {
		t.Fatalf("truncated output is %d bytes, want it near the 101-byte budget", len(out))
	}
}

func TestStartUserShellWithoutManager(t *testing.T) {
	if _, err := StartUserShell(nil, t.TempDir(), "ls"); err == nil {
		t.Fatal("StartUserShell with a nil manager succeeded, want an error")
	}
}

func TestStartUserShellRunsAndCapturesOutput(t *testing.T) {
	requireUserShell(t)
	mgr := execsess.NewManager()
	defer mgr.KillAll()
	run, err := StartUserShell(mgr, t.TempDir(), "echo hello && exit 3")
	if err != nil {
		t.Fatalf("StartUserShell: %v", err)
	}
	outcome := run.Wait(context.Background())
	if outcome.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", outcome.ExitCode)
	}
	if !strings.Contains(outcome.Output, "hello") {
		t.Errorf("Output = %q, want it to contain hello", outcome.Output)
	}
	if outcome.Aborted || outcome.TimedOut {
		t.Errorf("settled outcome = %+v, want a plain exit", outcome)
	}
}

func TestUserShellWaitCancelAborts(t *testing.T) {
	requireUserShell(t)
	mgr := execsess.NewManager()
	defer mgr.KillAll()
	run, err := StartUserShell(mgr, t.TempDir(), "sleep 30")
	if err != nil {
		t.Fatalf("StartUserShell: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	outcome := run.Wait(ctx)
	if !outcome.Aborted {
		t.Errorf("Aborted = false, want true (%+v)", outcome)
	}
	if outcome.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", outcome.ExitCode)
	}
	if outcome.Output != "command aborted by user" {
		t.Errorf("Output = %q, want %q", outcome.Output, "command aborted by user")
	}
	if outcome.Duration != 0 {
		t.Errorf("Duration = %v, want 0 for an aborted command", outcome.Duration)
	}
}
