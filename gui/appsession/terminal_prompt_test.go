package appsession

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/runtime"
)

type promptTerminalTool struct{ identity byte }

func (*promptTerminalTool) Name() string            { return "terminal" }
func (*promptTerminalTool) Description() string     { return "test terminal" }
func (*promptTerminalTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*promptTerminalTool) Execute(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{}, nil
}

func (*promptTerminalTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func TestGUITerminalGuideExplainsDefaultAndExplicitSystemTerminal(t *testing.T) {
	for _, text := range []string{
		"inside Golder GUI", "打开终端", "新建终端", "use the terminal tool, not computer_request",
		"action=new", "list/focus", "split", "user explicitly requests that system terminal",
		"noninteractive command execution, keep using bash", "read/input grants", "PTY-denial",
		"do not silently fall back to an external desktop terminal",
	} {
		if !strings.Contains(guiTerminalGuide, text) {
			t.Errorf("GUI terminal guide missing %q", text)
		}
	}
}

func TestGUITerminalGuideSurvivesCatalogPromptRebuildWithoutDuplication(t *testing.T) {
	terminal := &promptTerminalTool{}
	rebuilds := 0
	cfg := runtime.RunConfig{SyncTools: func(_ context.Context, ac *agentcore.AgentContext) {
		rebuilds++
		ac.SystemPrompt = "custom instructions, environment, and refreshed skills"
		ac.Tools = []agentcore.AgentTool{terminal}
	}}
	installGUITerminalPrompt(&cfg, terminal)
	ac := &agentcore.AgentContext{SystemPrompt: "old saved instructions"}
	for range 3 {
		cfg.SyncTools(context.Background(), ac)
		if !strings.HasPrefix(ac.SystemPrompt, "custom instructions, environment, and refreshed skills") || strings.Count(ac.SystemPrompt, guiTerminalGuide) != 1 {
			t.Fatalf("catalog rebuild lost or duplicated GUI guidance: %q", ac.SystemPrompt)
		}
	}
	if rebuilds != 3 || len(ac.Messages) != 0 {
		t.Fatal("guidance must preserve catalog sync and never add conversation messages")
	}
	plain := runtime.RunConfig{}
	installGUITerminalPrompt(&plain, terminal)
	for range 3 {
		plain.SyncTools(context.Background(), ac)
	}
	if strings.Count(ac.SystemPrompt, guiTerminalGuide) != 1 {
		t.Fatal("ordinary turns repeatedly appended the guidance")
	}
	// A different tool under the same name must not advertise our GUI panel.
	ac.Tools = []agentcore.AgentTool{&promptTerminalTool{}}
	plain.SyncTools(context.Background(), ac)
	if strings.Contains(ac.SystemPrompt, guiTerminalGuide) || ac.SystemPrompt != "custom instructions, environment, and refreshed skills" {
		t.Fatal("removed GUI tool retained stale frontend guidance")
	}
	ac.Tools = []agentcore.AgentTool{terminal}
	plain.SyncTools(context.Background(), ac)
	ac.Tools = nil
	plain.SyncTools(context.Background(), ac)
	if strings.Contains(ac.SystemPrompt, guiTerminalGuide) {
		t.Fatal("no-tools context retained stale GUI terminal guidance")
	}
}

func TestGUITerminalGuideOnlyInstalledForLiveGUIRuntime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gui       bool
		tools     bool
		install   bool
		wantGuide bool
	}{
		{"GUI with terminal", true, true, true, true},
		{"GUI before terminal registration", true, true, false, false},
		{"GUI no tools", true, false, true, false},
		{"non-GUI controller", false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOLDER_HOME", t.TempDir())
			opts := Options{GUI: tc.gui, Cwd: t.TempDir(), ProviderName: "fake", Model: "test", SysPrompt: "user's custom system instructions"}
			if tc.tools {
				opts.Tools = []agentcore.AgentTool{&agenttool.TodoTool{Store: agenttool.NewTodoStore()}}
			}
			c := New(opts)
			if err := c.Open(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if tc.install {
				_, err := c.InstallGUITerminalTool(c.SessionID(), c.cwd, &promptTerminalTool{})
				if (err == nil) != tc.wantGuide {
					t.Fatalf("GUI tool installation err=%v, want available=%t", err, tc.wantGuide)
				}
			}
			for _, source := range []string{"user's custom system instructions", "restored session's system instructions"} {
				c.agentCtx.SystemPrompt = source
				cfg := c.buildConfig()
				if cfg.SyncTools != nil {
					cfg.SyncTools(context.Background(), c.agentCtx)
				}
				if strings.Contains(c.agentCtx.SystemPrompt, guiTerminalGuide) != tc.wantGuide || !strings.HasPrefix(c.agentCtx.SystemPrompt, source) {
					t.Fatalf("incorrect live frontend guidance: %q", c.agentCtx.SystemPrompt)
				}
			}
			if c.header.SystemPrompt != opts.SysPrompt {
				t.Fatal("frontend guidance must not replace saved user-authored system instructions")
			}
		})
	}
}
