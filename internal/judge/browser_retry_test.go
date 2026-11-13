package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func browserPriorMessages(state string) agentcore.MessageList {
	return agentcore.MessageList{
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{
			agentcore.ToolCallContent{Type: "toolCall", ID: "prior", Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@e783"}`)},
		}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "browser", ToolCallID: "prior", IsError: true,
			Details: map[string]any{"execution_state": state}, Content: agentcore.ContentList{agentcore.NewTextContent("browser call ended")}},
	}
}

func TestUnknownBrowserDispatchCannotBeReplayedAutomatically(t *testing.T) {
	call, evidence, _ := browserAutoFixture(t)
	msgs := append(browserPriorMessages("unknown"), agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("继续")}})
	ctx := agentcore.WithMessageSnapshot(context.Background(), msgs)
	ctx = agentcore.WithBuiltinReview(ctx, call.Name, call.Arguments)
	ctx = agentcore.WithToolReviewEvidence(ctx, call.Name, call.Arguments, evidence)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true}},
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("auto asked a person")
			return ConsentAnswer{}
		}})
	d := gate(ctx, call)
	if d == nil || !d.Block || d.Details == nil || !strings.Contains(agentcore.ContentToText(*d.Content), "browser_prior_outcome_unknown") {
		t.Fatalf("unknown action was repeated: %+v", d)
	}
	details, ok := (*d.Details).(map[string]any)
	if !ok || details["execution_state"] != "not_dispatched" || details["recovery"] != "inspect_business_result" {
		t.Fatalf("wrong retry diagnostic: %+v", d.Details)
	}
}

func TestNotDispatchedAndDifferentBrowserActionsDoNotInheritUnknownOutcome(t *testing.T) {
	call, _, _ := browserAutoFixture(t)
	for _, state := range []string{"not_dispatched", ""} {
		ctx := agentcore.WithMessageSnapshot(context.Background(), browserPriorMessages(state))
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true}}})
		if d := gate(ctx, call); d == nil || d.Block {
			t.Fatalf("state %q was treated as unknown dispatch: %+v", state, d)
		}
	}
	other := call
	other.Arguments = json.RawMessage(`{"action":"click","ref":"@other"}`)
	if priorBrowserUnknown(other, browserPriorMessages("unknown")) {
		t.Fatal("a different target inherited an unrelated unknown outcome")
	}
}
