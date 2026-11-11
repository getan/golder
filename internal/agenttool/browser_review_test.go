package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/permissions"
)

func browserReviewSnapshot(t *testing.T, document, label, parent string, duplicate bool) string {
	t.Helper()
	nodes := []map[string]any{{"ref": "@root", "role": "RootWebArea", "name": "Applications", "depth": 0}}
	for i := range 240 {
		nodes = append(nodes, map[string]any{"ref": fmt.Sprintf("@junk%d", i), "role": "generic", "name": strings.Repeat("background ", 8), "depth": 1})
	}
	nodes = append(nodes, map[string]any{"ref": "@row", "role": "row", "name": parent, "depth": 1},
		map[string]any{"ref": "@e210", "role": "cell", "name": label, "value": "private-field-value", "depth": 2})
	if duplicate {
		nodes = append(nodes, map[string]any{"ref": "@e211", "role": "cell", "name": label, "depth": 2})
	}
	data, err := json.Marshal(map[string]any{"snapshotId": document, "nodes": nodes, "truncated": false})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBrowserTargetReviewSurvivesLongSnapshotForGUIAndTUI(t *testing.T) {
	for _, frontend := range []string{"gui", "tui"} {
		t.Run(frontend, func(t *testing.T) {
			tool, f := regressionBrowser(t)
			tool.Control.Mode.Set(permissions.Auto)
			tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), isolated: frontend == "tui", browserRuntime: frontend == "gui", policy: controlPolicyStamp()}
			const label = "AI组知识库助手"
			f.replies["snapshot"] = browserReviewSnapshot(t, "document-1", label, "App ID cli_approved", false)
			cfg := newExecCfg(t, tool)
			reviewed := false
			cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				evidence, ok := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
				if !ok || !strings.Contains(string(evidence.Facts), label) || !strings.Contains(string(evidence.Facts), "cli_approved") || len(evidence.Facts) > 6000 {
					t.Fatalf("target-directed evidence missing or unbounded: %s", evidence.Facts)
				}
				if evidence.CanSandbox != (frontend == "tui") || strings.Contains(string(evidence.Facts), "private-field-value") || strings.Contains(string(evidence.Facts), "background background") {
					t.Fatalf("runtime mismatch, input value or whole tree leaked: %s", evidence.Facts)
				}
				reviewed = true
				return &agentcore.BeforeToolCallDecision{Network: true}
			}
			msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "click", Name: "browser", Arguments: json.RawMessage(`{"action":"click","role":"cell","name":"AI组知识库助手","exact":true}`)}, nil)
			if msg.IsError || !reviewed {
				t.Fatalf("reviewed click failed: %s", textOf(msg))
			}
			mutations := 0
			for _, action := range f.actions {
				if action["action"] == "click" {
					mutations++
					p := action["params"].(map[string]any)
					if p["ref"] != "@e210" || p["exact"] != true {
						t.Fatalf("reviewed target was not pinned: %v", p)
					}
				}
			}
			if mutations != 1 {
				t.Fatalf("mutation count=%d", mutations)
			}
		})
	}
}

func TestBrowserReviewedTargetChangesAndAmbiguityNeverDispatch(t *testing.T) {
	for _, change := range []string{"document", "label", "parent", "ambiguous", "initial_ambiguous", "url", "target"} {
		t.Run(change, func(t *testing.T) {
			tool, f := regressionBrowser(t)
			f.replies["snapshot"] = browserReviewSnapshot(t, "document-1", "Target", "App cli_A", change == "initial_ambiguous")
			cfg := newExecCfg(t, tool)
			cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				switch change {
				case "document":
					f.replies["snapshot"] = browserReviewSnapshot(t, "document-2", "Target", "App cli_A", false)
				case "label":
					f.replies["snapshot"] = browserReviewSnapshot(t, "document-1", "Other target", "App cli_A", false)
				case "parent":
					f.replies["snapshot"] = browserReviewSnapshot(t, "document-1", "Target", "App cli_B", false)
				case "ambiguous":
					f.replies["snapshot"] = browserReviewSnapshot(t, "document-1", "Target", "App cli_A", true)
				case "url":
					f.info = strings.Replace(f.info, "https://example.test/reports", "https://other.test/", 1)
				case "target":
					f.info = strings.Replace(f.info, "business-target", "replacement-target", 1)
				}
				return &agentcore.BeforeToolCallDecision{Network: true}
			}
			msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "changed", Name: "browser", Arguments: json.RawMessage(`{"action":"click","role":"cell","name":"Target","exact":true}`)}, nil)
			if !msg.IsError {
				t.Fatal("changed or ambiguous target was executed")
			}
			for _, action := range f.actions {
				if action["action"] == "click" {
					t.Fatal("input was dispatched before revalidation")
				}
			}
			if strings.Contains(allText(msg), "may have been dispatched") {
				t.Fatal("deterministic pre-dispatch failure was labeled unknown")
			}
		})
	}
}

func TestBrowserGuardErrorsDistinguishOwnershipAndContainment(t *testing.T) {
	guard := NewControlGuard(t.TempDir())
	guard.Mode.Set(permissions.Auto)
	guard.rememberNativeBrowser("owned", "stub", "shared", browserProxyModeDirect)
	for _, tc := range []struct {
		id, socket, code string
		ctx              context.Context
		host             bool
	}{
		{"owned", "stub", "browser_outer_sandbox_unavailable", agentcore.WithSandboxRequest(context.Background()), false},
		{"owned", "other", "browser_socket_mismatch", agentcore.WithHostControlGrant(context.Background()), true},
		{"external", "stub", "browser_not_owned", context.Background(), false},
	} {
		err := guard.checkBrowser(tc.ctx, tc.id, tc.socket, tc.host)
		var typed *browserctl.Error
		if !errors.As(err, &typed) || typed.Code != tc.code {
			t.Fatalf("wanted %s, got %v", tc.code, err)
		}
	}
	if err := guard.checkBrowser(agentcore.WithHostControlGrant(context.Background()), "external", "stub", true); err != nil {
		t.Fatalf("exact external grant rejected: %v", err)
	}
	owned := guard.browsers["owned"]
	owned.policy[0] ^= 0xff
	guard.browsers["owned"] = owned
	var typed *browserctl.Error
	if err := guard.checkBrowser(context.Background(), "owned", "stub", false); !errors.As(err, &typed) || typed.Code != "browser_policy_changed" {
		t.Fatalf("policy change lost: %v", err)
	}
}

func TestBrowserExactIsForwardedForEveryLocatorAction(t *testing.T) {
	for _, action := range []string{"find", "get", "is", "wait", "click", "dblclick", "hover", "focus", "check", "uncheck", "scroll_into_view", "fill", "type", "select"} {
		_, params, err := browserActionParams(browserArgs{Action: action, Role: "button", Name: "Target", Text: "value", Value: "v", Kind: "text", Visible: "visible", Exact: true})
		if err != nil || params["exact"] != true {
			t.Errorf("%s dropped exact: %v %v", action, params, err)
		}
	}
	for _, args := range []browserArgs{
		{Action: "press", Key: "Enter", Exact: true},
		{Action: "get", Kind: "url", Exact: true},
		{Action: "wait", URLPattern: "https://example.com", Exact: true},
	} {
		if _, _, err := browserActionParams(args); err == nil {
			t.Fatal("unsupported exact was silently ignored")
		}
	}
}
