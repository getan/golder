package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	for _, raw := range []string{
		`{"action":"run","command":"go test ./..."}`,
		`{"action":"run","script":"hostname\nid\n","remote":true}`,
	} {
		spy := &firstPartySpy{v: Verdict{Level: Allow, Source: "local"}}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios", json.RawMessage(raw))
		if spy.tool != "tuios" || string(spy.args) != raw {
			t.Fatalf("next call = %q %s, want the original tuios call %s", spy.tool, spy.args, raw)
		}
		if v.Source != "local" {
			t.Errorf("run verdict = %+v, want the local grader's", v)
		}
	}
}

func TestFirstPartyRunRequiresExactlyOneCommandOrScript(t *testing.T) {
	for _, tc := range []struct {
		raw, reason string
	}{
		{`{"action":"run"}`, "no command or script"},
		{`{"action":"run","command":"id","script":"hostname"}`, "cannot use command and script together"},
		{`{"action":"run","command":"id","script":"hostname","host_control":true}`, "cannot use command and script together"},
	} {
		spy := &firstPartySpy{v: Verdict{Level: Allow}}
		v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios", json.RawMessage(tc.raw))
		if !v.Failed || spy.tool != "" || len(v.Reasons) == 0 || !strings.Contains(v.Reasons[0], tc.reason) {
			t.Errorf("%s = %+v next=%q, want fail-closed containing %q", tc.raw, v, spy.tool, tc.reason)
		}
	}
}

func TestFirstPartyScriptStillUsesStaticFloor(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"run","script":"cat ~/.ssh/id_rsa"}`))
	if v.Level != Deny || spy.tool != "" {
		t.Fatalf("credential-reading script bypassed static floor: verdict=%+v next=%q", v, spy.tool)
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
	for _, action := range []string{"open", "attach", "click", "fill", "type", "press", "navigate", "quit"} {
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

func TestFirstPartyComputerRequestAndActions(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "reviewer"}}
	classifier := FirstParty{Next: spy}
	if v := classifier.Classify(context.Background(), "computer_request", json.RawMessage(`{"application":"Calculator"}`)); v.Level != Confirm || spy.tool != "" {
		t.Fatalf("application request = %+v, next=%q", v, spy.tool)
	}
	for _, action := range []string{"status", "snapshot", "screenshot", "pause", "stop"} {
		v := classifier.Classify(context.Background(), "computer", json.RawMessage(fmt.Sprintf(`{"action":%q}`, action)))
		if v.Level != Allow || spy.tool != "" {
			t.Fatalf("%s = %+v, next=%q", action, v, spy.tool)
		}
	}
	v := classifier.Classify(context.Background(), "computer", json.RawMessage(`{"action":"click","snapshot_id":"s","ref":"@e1"}`))
	if v.Level != Deny || spy.tool != "computer" {
		t.Fatalf("control bypassed contextual review: %+v, next=%q", v, spy.tool)
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
