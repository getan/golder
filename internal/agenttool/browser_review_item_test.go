package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestReviewRenderedPermissionItem(t *testing.T) {
	const snapshot = `{"snapshotId":"doc","truncated":false,"parentLinks":true,"nodes":[{"ref":"@root","role":"RootWebArea","depth":0},{"ref":"@row","parentRef":"@root","role":"row","depth":1},{"ref":"@check","parentRef":"@row","role":"checkbox","depth":2},{"ref":"@label","parentRef":"@row","role":"LabelText","depth":2},{"ref":"@api1","parentRef":"@row","role":"link","depth":2},{"ref":"@api2","parentRef":"@row","role":"link","depth":2},{"ref":"@more","parentRef":"@row","role":"button","depth":2}]}`
	for _, tc := range []struct {
		name, markup, text, changed, want string
	}{
		{"permission", `<div><input type="checkbox"><span>搜索云文档 search:docs:read</span><a>API</a><button>更多</button></div>`, "搜索云文档 search:docs:read API 更多", "", "success"},
		{"changed", `<div><input type="checkbox"><span>搜索云文档</span></div>`, "search:docs:read", "search:message", "browser_target_changed"},
		{"password", `<div><input type="checkbox"><input type="password"></div>`, "search:docs:read", "", "browser_target_context_unavailable"},
		{"textarea", `<div><input type="checkbox"><textarea></textarea></div>`, "search:docs:read", "", "browser_target_context_unavailable"},
		{"unobserved second input", `<div><input type="checkbox"><input type="checkbox"></div>`, "search:docs:read", "", "browser_target_context_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, f := regressionBrowser(t)
			f.replies["snapshot"], f.replies["is"] = snapshot, `true`
			markup, _ := json.Marshal(tc.markup)
			text, _ := json.Marshal(tc.text)
			f.getReplies = map[string]string{"html": string(markup), "text": string(text)}
			cfg := newExecCfg(t, tool)
			cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				ev, _ := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
				if tc.want == "success" && (!strings.Contains(string(ev.Facts), "same_item_rendered_text") || !strings.Contains(string(ev.Facts), "search:docs:read")) {
					t.Fatalf("missing bound row evidence: %s", ev.Facts)
				}
				if tc.changed != "" {
					changed, _ := json.Marshal(tc.changed)
					f.getReplies["text"] = string(changed)
				}
				return &agentcore.BeforeToolCallDecision{Network: true}
			}
			msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "check", Name: "browser", Arguments: json.RawMessage(`{"action":"check","ref":"@check"}`)}, nil)
			if tc.want == "success" && msg.IsError || tc.want != "success" && (!msg.IsError || !strings.Contains(textOf(msg), tc.want)) {
				t.Fatalf("unexpected result: %s", textOf(msg))
			}
			dispatched := false
			for _, action := range f.actions {
				if action["action"] == "check" {
					dispatched = true
				}
			}
			if dispatched != (tc.want == "success") {
				t.Fatalf("check dispatch = %v", dispatched)
			}
		})
	}
}

func TestModernHostUnnamedContainerRenderedText(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"doc","truncated":false,"parentLinks":true,"nodes":[{"ref":"@root","role":"RootWebArea","depth":0},{"ref":"@tab","parentRef":"@root","role":"generic","depth":1}]}`
	f.replies["is"] = `true`
	f.getReplies = map[string]string{"html": `"<div>用户身份权限</div>"`, "text": `"用户身份权限"`}
	args, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"click","ref":"@tab"}`))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := tool.ReviewEvidence(context.Background(), args)
	if err != nil || !strings.Contains(string(ev.Facts), "target_rendered_text") || !strings.Contains(string(ev.Facts), "用户身份权限") {
		t.Fatalf("missing target evidence: %s %v", ev.Facts, err)
	}
}

func TestBrowserCanInspectAndRetargetAfterMissingContext(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"doc","truncated":false,"nodes":[{"ref":"@check","role":"checkbox","depth":0}]}`
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	first, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "first", Name: "browser", Arguments: json.RawMessage(`{"action":"check","ref":"@check"}`)}, nil)
	if !first.IsError || !strings.Contains(textOf(first), "browser_target_context_unavailable") || !strings.Contains(textOf(first), "not a browser-wide stop") {
		t.Fatalf("missing safe recovery: %s", textOf(first))
	}
	for _, action := range f.actions {
		if action["action"] == "check" {
			t.Fatal("unidentified control received input")
		}
	}
	// A subsequent observation can establish a distinct, named target. It is
	// a new call with a new review, not a replay of the failed input.
	f.replies["snapshot"] = `{"snapshotId":"doc","truncated":false,"parentLinks":true,"nodes":[{"ref":"@named","role":"checkbox","name":"搜索云文档 search:docs:read","depth":0}]}`
	observed, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "inspect", Name: "browser", Arguments: json.RawMessage(`{"action":"snapshot"}`)}, nil)
	if observed.IsError {
		t.Fatalf("read-only inspection was blocked: %s", textOf(observed))
	}
	second, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "second", Name: "browser", Arguments: json.RawMessage(`{"action":"check","ref":"@named"}`)}, nil)
	if second.IsError {
		t.Fatalf("newly reviewed target was blocked: %s", textOf(second))
	}
	checks := 0
	for _, action := range f.actions {
		if action["action"] == "check" {
			checks++
			if action["params"].(map[string]any)["ref"] != "@named" {
				t.Fatalf("wrong target dispatched: %v", action)
			}
		}
	}
	if checks != 1 {
		t.Fatalf("check dispatches = %d, want one independently reviewed target", checks)
	}
}
