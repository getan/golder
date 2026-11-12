package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

func TestAutoBrowserRemoteEffectRevalidatesBeforeDispatch(t *testing.T) {
	for _, change := range []string{"none", "label", "url", "paused"} {
		t.Run(change, func(t *testing.T) {
			tool, f := regressionBrowser(t)
			tool.Control.Mode.Set(permissions.Auto)
			tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), browserRuntime: true, policy: controlPolicyStamp()}
			f.replies["snapshot"] = browserReviewSnapshot(t, "doc", "sync 立即同步", "Current task", false)
			reviewer := judge.NewLLMJudge(func(_ context.Context, prompt, state string) (string, error) {
				if !strings.Contains(state, "立即同步") || !strings.Contains(state, "列名已恢复") || !strings.Contains(prompt, "remote_effect_only") {
					t.Fatalf("reviewer did not receive target and intent: %s", state)
				}
				switch change {
				case "label":
					f.replies["snapshot"] = browserReviewSnapshot(t, "doc", "delete task", "Current task", false)
				case "url":
					f.info = strings.Replace(f.info, "https://example.test/reports", "https://example.test/other", 1)
				case "paused":
					f.replies["interaction.status"] = `{"paused":true,"pauseGeneration":1}`
				}
				return `{"level":"contain","risk":"medium","authorization":"high","rationale":"sync the requested task","network":"allow","browser_containment":"remote_effect_only"}`, nil
			}, t.TempDir(), true)
			cfg := newExecCfg(t, tool)
			cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: reviewer,
				Sandboxed: func(string) bool { return true },
				Confirm: func(context.Context, judge.ConsentRequest) judge.ConsentAnswer {
					t.Fatal("auto asked a person")
					return judge.ConsentAnswer{}
				},
			})
			ctx := agentcore.WithMessageSnapshot(context.Background(), agentcore.MessageList{
				agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("列名已恢复，请对当前任务点击立即同步")}},
			})
			msg, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "sync", Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@e210"}`)}, nil)
			var dispatched int
			for _, action := range f.actions {
				if action["action"] == "click" {
					dispatched++
				}
			}
			if change == "none" {
				if msg.IsError || dispatched != 1 {
					t.Fatalf("bound sync: dispatched %d, %s", dispatched, allText(msg))
				}
			} else if !msg.IsError || dispatched != 0 {
				t.Fatalf("%s dispatched %d actions: %s", change, dispatched, allText(msg))
			}
		})
	}
}
