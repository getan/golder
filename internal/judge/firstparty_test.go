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

func TestFirstPartyRunIsGradedAsBash(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow, Source: "local"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios",
		json.RawMessage(`{"action":"run","command":"go test ./..."}`))
	if spy.tool != "bash" {
		t.Fatalf("next tool = %q, want bash", spy.tool)
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
