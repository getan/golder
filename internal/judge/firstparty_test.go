package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

type firstPartySpy struct {
	tool string
	args json.RawMessage
	v    Verdict
}

func (s *firstPartySpy) Classify(_ context.Context, tool string, args json.RawMessage) Verdict {
	s.tool, s.args = tool, args
	return s.v
}

func TestFirstPartyReadToolsAreLocalAllows(t *testing.T) {
	for _, action := range []string{"list", "capture", "wait"} {
		spy := &firstPartySpy{}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
			json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if spy.tool != "" {
			t.Errorf("%s reached the next grader", action)
		}
		if v.Level != Allow || v.Source != LocalSource {
			t.Errorf("%s = %+v, want a local allow", action, v)
		}
	}
}

func TestFirstPartyRunPreservesBrokerIdentity(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow, Source: "local"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"run","command":"go test ./..."}`))
	if spy.tool != "tuios" {
		t.Fatalf("next tool = %q, want the original tuios call", spy.tool)
	}
	if v.Source != "local" {
		t.Errorf("run verdict = %+v, want the local grader's", v)
	}
}

func TestFirstPartyCloseNeedsReviewer(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "llm"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"close","window":"w2"}`))
	if spy.tool != "tuios" {
		t.Fatalf("next tool = %q, want tuios", spy.tool)
	}
	if v.Level != Deny {
		t.Errorf("close verdict = %+v, want the reviewer's deny", v)
	}
}

func TestFirstPartyPaneMutationsAreLocalConfirms(t *testing.T) {
	for _, action := range []string{"move", "resize", "rename", "minimize"} {
		spy := &firstPartySpy{}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
			json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if spy.tool != "" {
			t.Errorf("%s reached the next grader", action)
		}
		if v.Level != Confirm || v.Source != LocalSource {
			t.Errorf("%s = %+v, want a local confirm", action, v)
		}
	}
}

func TestFirstPartySendNeedsTheNextGrader(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "llm"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"send","text":"yes"}`))
	if spy.tool != "tuios" {
		t.Fatalf("next tool = %q, want tuios", spy.tool)
	}
	if v.Level != Deny {
		t.Errorf("send verdict = %+v, want the reviewer's deny", v)
	}
}

// A browser read changes nothing, so it does not spend a reviewer round trip.
func TestFirstPartyBrowserReadsAreLocalAllows(t *testing.T) {
	for _, action := range []string{"list", "snapshot", "find", "get", "is", "wait", "console", "errors", "network"} {
		spy := &firstPartySpy{}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "browser",
			json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if spy.tool != "" {
			t.Errorf("%s reached the next grader", action)
		}
		if v.Level != Allow || v.Source != LocalSource {
			t.Errorf("%s = %+v, want a local allow", action, v)
		}
	}
}

// Typing into a real page is the reviewer's call, not a fixed table's.
func TestFirstPartyBrowserWritesNeedTheNextGrader(t *testing.T) {
	for _, action := range []string{"open", "attach", "click", "fill", "type", "press", "navigate"} {
		spy := &firstPartySpy{v: Verdict{Level: Contain, Source: "llm"}}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "browser",
			json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if spy.tool != "browser" {
			t.Fatalf("%s did not reach the next grader", action)
		}
		if v.Level != Contain {
			t.Errorf("%s verdict = %+v, want the reviewer's", action, v)
		}
	}
}

// eval and upload are outside the tool's schema; a call carrying one is denied
// without spending a reviewer round trip.
func TestFirstPartyBrowserEvalIsDenied(t *testing.T) {
	for _, action := range []string{"eval", "upload"} {
		spy := &firstPartySpy{}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "browser",
			json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if spy.tool != "" {
			t.Errorf("%s reached the next grader", action)
		}
		if v.Level != Deny {
			t.Errorf("%s = %+v, want deny", action, v)
		}
	}
}

// press and screenshot were added to the tool after this table was written, so
// they fell through to "unknown tuios action" and were blocked. A picture is a
// write to a project file; both actions belong to the reviewer.
func TestFirstPartyTuiosPressAndScreenshot(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow, Source: "llm"}}
	if v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"press","key":"Enter"}`)); v.Level != Allow {
		t.Errorf("press = %+v, want the reviewer's allow", v)
	}
	if spy.tool != "tuios" {
		t.Fatalf("press did not reach the next grader")
	}

	local := &firstPartySpy{v: Verdict{Level: Allow, Source: "llm"}}
	v := (FirstParty{Next: local}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"screenshot"}`))
	if local.tool != "tuios" {
		t.Errorf("screenshot skipped write review")
	}
	if v.Level != Allow || v.Source != "llm" {
		t.Errorf("screenshot = %+v, want the reviewer's verdict", v)
	}
}
