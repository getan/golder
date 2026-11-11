package agentcore

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolReviewEvidenceCannotBeReusedForDifferentArguments(t *testing.T) {
	args := json.RawMessage(`{"action":"click","ref":"@e210"}`)
	facts := json.RawMessage(`{"match_count":1}`)
	ctx := WithToolReviewEvidence(context.Background(), "browser", args, ToolReviewEvidence{Facts: facts})
	facts[0] = '['
	got, ok := ToolReviewEvidenceFromContext(ctx, "browser", args)
	if !ok || string(got.Facts) != `{"match_count":1}` {
		t.Fatal("evidence was not copied")
	}
	got.Facts[0] = '['
	if again, _ := ToolReviewEvidenceFromContext(ctx, "browser", args); string(again.Facts) != `{"match_count":1}` {
		t.Fatal("getter exposed mutable evidence")
	}
	for _, tc := range []struct{ name, args string }{{"browser", `{"action":"click","ref":"@e211"}`}, {"bash", string(args)}} {
		if _, ok := ToolReviewEvidenceFromContext(ctx, tc.name, json.RawMessage(tc.args)); ok {
			t.Fatal("unrelated tool call inherited evidence")
		}
	}
}
