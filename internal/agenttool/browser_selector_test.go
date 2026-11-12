package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestUnsupportedBrowserSelectorSyntax(t *testing.T) {
	for _, tc := range []struct {
		selector string
		want     bool
	}{
		{`.ud__tabs__tab:has-text("用户身份权限")`, true},
		{`text=用户身份权限`, true},
		{`.item >> nth=0`, true},
		{`:text("hello")`, true},
		{`/pattern/`, true},
		{`.ud__tabs__tab:not(.ud__tabs__tab--active)`, false},
		{`[data-value="text=foo >> :has-text("]`, false},
		{`.escaped\:has-text`, false},
	} {
		if got := unsupportedBrowserSelector(tc.selector); got != tc.want {
			t.Errorf("unsupportedBrowserSelector(%q) = %v", tc.selector, got)
		}
	}
	tool, f := regressionBrowser(t)
	_, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"click","selector":"text=example"}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported_selector_syntax") {
		t.Fatalf("unexpected preparation result: %v", err)
	}
	if len(f.actions) != 0 {
		t.Fatalf("invalid selector contacted host: %v", f.actions)
	}
}

func TestMissingBrowserRefIsReportedBeforeReview(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"new-document","truncated":false,"parentLinks":true,"nodes":[{"ref":"@other","role":"button","name":"other","depth":0}]}`
	cfg := newExecCfg(t, tool)
	reviewed := false
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		reviewed = true
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "click", Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@old"}`)}, nil)
	if !msg.IsError || !strings.Contains(textOf(msg), "browser_ref_not_in_snapshot") || reviewed {
		t.Fatalf("missing ref should be rejected before review: reviewed=%v %s", reviewed, textOf(msg))
	}
	for _, action := range f.actions {
		if action["action"] == "click" {
			t.Fatal("missing ref received input")
		}
	}
}
