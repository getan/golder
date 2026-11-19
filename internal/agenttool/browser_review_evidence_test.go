package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestCSSAriaLabelIsObservedButStillDenied(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"doc","nodes":[],"truncated":false}`
	f.replies["is"] = `true`
	markup, _ := json.Marshal(`<button></button>`)
	text, _ := json.Marshal("")
	f.getReplies = map[string]string{
		"count": `1`, "html": string(markup), "text": string(text), "attr": `"下一首"`,
	}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg,
		agentcore.AgentToolCall{ID: "click", Name: "browser", Arguments: json.RawMessage(`{"action":"click","selector":"button[aria-label=\"下一首\"]"}`)}, nil)
	if !msg.IsError || !strings.Contains(textOf(msg), "CSS locator did not establish") {
		t.Fatalf("CSS aria-label was not handled as unobserved name evidence: %s", textOf(msg))
	}
	var facts browserReviewFacts
	data, _ := json.Marshal(msg.Details)
	if err := json.Unmarshal(data, &facts); err != nil {
		t.Fatalf("decode details: %v (%s)", err, data)
	}
	if facts.Observed == nil || facts.Observed.AriaLabel != "下一首" {
		t.Fatalf("observed aria-label missing: %s", data)
	}
	if facts.Suggested == nil || !facts.Suggested.RequiresReview || facts.Suggested.SnapshotID != "doc" ||
		facts.Suggested.Locator["name"] != "下一首" {
		t.Fatalf("suggested locator missing or not bound to the snapshot: %s", data)
	}
	for _, action := range f.actions {
		if action["action"] == "click" {
			t.Fatal("denied CSS target received input")
		}
	}
}

func TestHiddenTargetSuggestionCarriesVisibleParent(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"document-2","truncated":false,"parentLinks":true,"nodes":[
		{"ref":"@root","role":"RootWebArea","depth":0},
		{"ref":"@label","parentRef":"@root","role":"LabelText","name":"原生逐条标注","depth":1,"visible":true},
		{"ref":"@check","parentRef":"@label","role":"radio","name":"native","depth":2,"visible":false}
	]}`
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg,
		agentcore.AgentToolCall{ID: "click", Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@check"}`)}, nil)
	if !msg.IsError || !strings.Contains(textOf(msg), "a visible associated label may exist") {
		t.Fatalf("hidden target message did not explain the visible-label possibility: %s", textOf(msg))
	}
	var facts browserReviewFacts
	data, _ := json.Marshal(msg.Details)
	if err := json.Unmarshal(data, &facts); err != nil {
		t.Fatalf("decode details: %v (%s)", err, data)
	}
	if facts.Suggested == nil || !facts.Suggested.RequiresReview || facts.Suggested.SnapshotID != "document-2" ||
		len(facts.Suggested.Candidates) != 1 {
		t.Fatalf("hidden-target suggestion missing: %s", data)
	}
	got := facts.Suggested.Candidates[0]
	if got.Ref != "@label" || got.Role != "LabelText" || got.Name != "原生逐条标注" ||
		got.Relation != "ancestor_label" || got.Confidence != "unique" {
		t.Fatalf("suggested candidate = %+v", got)
	}
	for _, action := range f.actions {
		if action["action"] == "click" {
			t.Fatal("hidden target received input")
		}
	}
}
