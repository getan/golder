package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserbinding"
	"github.com/getan/golder/internal/browserctl"
)

// GUI and TUI operators both update ActiveTabID on their conversation's host.
// The Agent must use that same foreground, without sticky tool-local tab state.
func TestConversationDefaultFollowsGUIAndTUIForeground(t *testing.T) {
	for _, frontend := range []string{"gui", "tui"} {
		t.Run(frontend, func(t *testing.T) {
			tool, f := regressionBrowser(t)
			binding := ConfigureBrowserSession([]agentcore.AgentTool{tool}, frontend+"-conversation")
			binding.Select(browserbinding.Endpoint{Socket: "stub", BrowserID: "browser-1"})
			f.info = strings.Replace(f.info, "chrome://settings/help", "https://other.test/", 1)
			prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
			if err != nil {
				t.Fatal(err)
			}
			var args browserArgs
			_ = json.Unmarshal(prepared, &args)
			if args.Tab != "internal" || args.TargetID != "internal-target" || args.Socket != binding.Snapshot().Endpoint.Socket {
				t.Fatalf("Agent did not follow %s foreground: %+v", frontend, args)
			}
			// A later manual switch must not redirect an already prepared call.
			f.info = strings.ReplaceAll(f.info, `"activeTabId":"internal"`, `"activeTabId":"business"`)
			f.info = strings.ReplaceAll(f.info, `,"active":true`, "")
			result, err := tool.Execute(context.Background(), "pinned", prepared, nil)
			if err != nil || result.IsError {
				t.Fatalf("pinned call failed: %s %v", agentcore.ContentToText(result.Content), err)
			}
			target := f.actions[0]["target"].(map[string]any)
			if target["tabId"] != "internal" || target["targetId"] != "internal-target" {
				t.Fatalf("prepared call was redirected: %+v", target)
			}
			current, _ := tool.newClient()
			info, _ := current.Info(context.Background())
			if next, err := tool.resolveTarget(info, ""); err != nil || next.TabID != "business" {
				t.Fatalf("next call did not adopt the new foreground: %+v %v", next, err)
			}
		})
	}
}

func TestConversationBackgroundTargetDoesNotSelectOrReveal(t *testing.T) {
	tool, f := regressionBrowser(t)
	binding := ConfigureBrowserSession([]agentcore.AgentTool{tool}, "conversation")
	binding.Select(browserbinding.Endpoint{Socket: "stub", BrowserID: "browser-1"})
	f.info = strings.Replace(f.info, "chrome://settings/help", "https://other.test/", 1)
	before := binding.Snapshot()
	result := runBrowserCall(t, tool, `{"action":"snapshot","tab":"business"}`)
	if result.IsError || binding.Snapshot().Revision != before.Revision {
		t.Fatalf("background observation changed binding: %s", agentcore.ContentToText(result.Content))
	}
	result = runBrowserCall(t, tool, `{"action":"navigate","tab":"business","url":"https://background.test/"}`)
	if result.IsError || binding.Snapshot().Revision != before.Revision {
		t.Fatalf("explicit background navigation revealed the drawer: %s", agentcore.ContentToText(result.Content))
	}
	info := browserctl.Info{}
	_ = json.Unmarshal([]byte(f.info), &info)
	if target, err := tool.resolveTarget(info, ""); err != nil || target.TabID != "internal" {
		t.Fatalf("background observation selected a tab: %+v %v", target, err)
	}
}

func TestReviewedConversationBrowserCannotBeRedirected(t *testing.T) {
	tool, _ := regressionBrowser(t)
	binding := ConfigureBrowserSession([]agentcore.AgentTool{tool}, "conversation")
	binding.Select(browserbinding.Endpoint{Socket: "stub", BrowserID: "browser-1"})
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"snapshot","tab":"business"}`))
	if err != nil {
		t.Fatal(err)
	}
	binding.Select(browserbinding.Endpoint{Socket: "other", BrowserID: "browser-2"})
	result, err := tool.Execute(context.Background(), "reviewed", prepared, nil)
	if err != nil || !result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), "binding changed") {
		t.Fatalf("reviewed browser was redirected: %+v %v", result, err)
	}
}

func TestConfigureBrowserSessionReturnsSameBindingUntilConversationChanges(t *testing.T) {
	tool := NewBrowserTool(t.TempDir(), true)
	tools := []agentcore.AgentTool{tool}
	first := ConfigureBrowserSession(tools, "first")
	if ConfigureBrowserSession(tools, "first") != first {
		t.Fatal("runtime reconfiguration replaced the GUI's shared binding")
	}
	first.Select(browserbinding.Endpoint{Socket: "first", BrowserID: "first"})
	second := ConfigureBrowserSession(tools, "second")
	if second == first || tool.socketPath() != "" || second.Snapshot().SessionID != "second" {
		t.Fatal("conversation switch reused another conversation's binding")
	}
}
