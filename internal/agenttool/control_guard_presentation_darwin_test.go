package agenttool

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// The child must be told how to draw by the terminal the pane is actually on.
// A fixed TERM=xterm-256color made every graphics program guess "no images".
func TestPresentationEnvironmentIsForwardedNotInvented(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS browser runtime")
	}
	t.Setenv("GOLDER_SANDBOX", "auto")
	for name, value := range map[string]string{
		"TERM": "xterm-ghostty", "COLORTERM": "truecolor",
		"TERM_PROGRAM": "ghostty", "TERM_PROGRAM_VERSION": "1.2.3",
		"OPENAI_API_KEY": "synthetic-not-a-real-key",
	} {
		t.Setenv(name, value)
	}
	g := NewControlGuard(t.TempDir())
	g.Runner = &controlRunner{}
	ctx := agentcore.WithBrowserRuntimeGrant(agentcore.WithNetworkGrant(context.Background()))
	argv, cleanup, _, err := g.launchArgvFor(ctx, []string{"golder", "browse"}, g.Root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := " " + strings.Join(argv, " ") + " "
	for _, want := range []string{
		" TERM=xterm-ghostty ", " COLORTERM=truecolor ",
		" TERM_PROGRAM=ghostty ", " TERM_PROGRAM_VERSION=1.2.3 ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatal("the scrubber forwarded a credential")
	}
}

// A value with a newline could add another assignment to env -i's argv.
func TestUnsafePresentationValueIsNotForwarded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS browser runtime")
	}
	t.Setenv("GOLDER_SANDBOX", "auto")
	t.Setenv("TERM", "xterm\nPATH=/tmp/evil")
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM_PROGRAM_VERSION", "")
	g := NewControlGuard(t.TempDir())
	g.Runner = &controlRunner{}
	ctx := agentcore.WithBrowserRuntimeGrant(agentcore.WithNetworkGrant(context.Background()))
	argv, cleanup, _, err := g.launchArgvFor(ctx, []string{"golder", "browse"}, g.Root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "PATH=/tmp/evil") {
		t.Fatalf("unsafe value reached the child: %q", joined)
	}
	if !strings.Contains(joined, "TERM=xterm-256color") {
		t.Fatalf("no safe TERM fallback: %q", joined)
	}
}
