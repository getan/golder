package appsession

import (
	"encoding/json"
	"testing"
)

func TestToolArgumentsDecodeRawMessage(t *testing.T) {
	args := decodeArgsAny(json.RawMessage(`{"action":"open","url":"https://example.com"}`))
	if args["action"] != "open" || args["url"] != "https://example.com" {
		t.Fatalf("raw tool arguments lost: %#v", args)
	}
}

func TestToolEventCloneDoesNotShareDetails(t *testing.T) {
	original := Item{Tool: &ToolState{Input: map[string]any{"action": "open"}, Details: map[string]any{"socket": "one"}}}
	copy := cloneItem(original)
	original.Tool.Input["action"] = "navigate"
	original.Tool.Details["socket"] = "two"
	if copy.Tool.Input["action"] != "open" || copy.Tool.Details["socket"] != "one" {
		t.Fatalf("tool event changed after cloning: %+v", copy.Tool)
	}
}
