package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

func TestBrowserSelectorDoesNotForwardExact(t *testing.T) {
	for _, action := range []string{"find", "get", "is", "wait", "click", "dblclick", "hover", "focus", "check", "uncheck", "scroll_into_view", "fill", "type", "select"} {
		for _, exact := range []bool{false, true} {
			args := browserArgs{Action: action, Selector: ".x", Exact: exact, Kind: "text", Visible: "visible", Text: "value", Value: "v"}
			_, params, err := browserActionParams(args)
			if err != nil {
				t.Fatalf("%s exact=%t: %v", action, exact, err)
			}
			if _, ok := params["exact"]; ok {
				t.Fatalf("%s exact=%t forwarded exact: %v", action, exact, params)
			}
		}
	}
}

func TestCSSReviewOmitsExactAndDoesNotDispatchAmbiguousTarget(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"doc","nodes":[],"truncated":false}`
	f.replies["get"] = `2`
	args, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"click","selector":".item","exact":true}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.ReviewEvidence(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var facts browserReviewFacts
	if json.Unmarshal(evidence.Facts, &facts) != nil || facts.MatchCount != 2 {
		t.Fatalf("unexpected evidence: %s", evidence.Facts)
	}
	gets := 0
	for _, action := range f.actions {
		if action["action"] == "get" {
			gets++
			if _, ok := action["params"].(map[string]any)["exact"]; ok {
				t.Fatal("CSS review forwarded exact")
			}
		}
	}
	if gets == 0 {
		t.Fatal("CSS review did not perform its count check")
	}
	ctx := agentcore.WithToolReviewEvidence(context.Background(), tool.Name(), args, evidence)
	var a browserArgs
	_ = json.Unmarshal(args, &a)
	c, err := tool.clientForSocket(tool.socketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	if err := tool.verifyReviewedLocator(ctx, c, a, args, map[string]any{}); err == nil || !strings.Contains(err.Error(), "ambiguous_locator") {
		t.Fatalf("ambiguous CSS should not dispatch: %v", err)
	}
}

func TestBrowserRecoveryHintsPreserveUnknownOutcome(t *testing.T) {
	for _, code := range []string{"not_interactable", "ambiguous_locator", "javascript_error"} {
		res := browserActionError("click", &browserctl.Error{Code: code, Message: "fixture"})
		text := agentcore.ContentToText(res.Content)
		if !strings.Contains(text, "inspect") || !strings.Contains(text, "may have been dispatched") {
			t.Fatalf("%s lost cautious recovery guidance: %s", code, text)
		}
	}
}

func TestReviewVisibilityIsOptionalEvidence(t *testing.T) {
	var node browserReviewNode
	if err := json.Unmarshal([]byte(`{"ref":"@a","role":"radio","visible":false}`), &node); err != nil || node.Visible == nil || *node.Visible {
		t.Fatal("explicit hidden evidence lost")
	}
	var unknown browserReviewNode
	_ = json.Unmarshal([]byte(`{"ref":"@a","role":"radio"}`), &unknown)
	if unknown.Visible != nil {
		t.Fatal("old host visibility must remain unknown")
	}
}

func TestAmbiguousReviewReportsVisibilityWithoutSelectingCandidate(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.replies["snapshot"] = `{"snapshotId":"doc","truncated":false,"nodes":[{"ref":"@a","role":"radio","name":"Choice","depth":1,"visible":false},{"ref":"@b","role":"StaticText","name":"Choice","depth":1,"visible":true}]}`
	args, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"click","text":"Choice","exact":true}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.ReviewEvidence(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var facts browserReviewFacts
	if err := json.Unmarshal(evidence.Facts, &facts); err != nil || facts.MatchCount != 2 || len(facts.Candidates) != 2 || facts.Candidates[0].Visible == nil || *facts.Candidates[0].Visible || facts.Candidates[1].Visible == nil || !*facts.Candidates[1].Visible {
		t.Fatalf("candidate visibility not preserved: %s (%v)", evidence.Facts, err)
	}
	ctx := agentcore.WithToolReviewEvidence(context.Background(), tool.Name(), args, evidence)
	var a browserArgs
	_ = json.Unmarshal(args, &a)
	c, err := tool.clientForSocket(tool.socketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	if err := tool.verifyReviewedLocator(ctx, c, a, args, map[string]any{}); err == nil || !strings.Contains(err.Error(), "ambiguous_locator") {
		t.Fatalf("visibility must not silently choose a target: %v", err)
	}
	for _, action := range f.actions {
		if action["action"] == "click" {
			t.Fatal("ambiguous candidate was clicked")
		}
	}
}
