package judge

import (
	"context"
	"encoding/json"
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
	for _, name := range []string{"tuios_list_windows", "tuios_capture_pane", "tuios_wait_for"} {
		spy := &firstPartySpy{}
		v := (FirstParty{Next: spy}).Classify(context.Background(), name, nil)
		if spy.tool != "" {
			t.Errorf("%s reached the next grader", name)
		}
		if v.Level != Allow || v.Source != LocalSource {
			t.Errorf("%s = %+v, want a local allow", name, v)
		}
	}
}

func TestFirstPartyRunIsGradedAsBash(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow, Source: "local"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios_run",
		json.RawMessage(`{"command":"go test ./..."}`))
	if spy.tool != "bash" {
		t.Fatalf("next tool = %q, want bash", spy.tool)
	}
	if v.Source != "local" {
		t.Errorf("run verdict = %+v, want the local grader's", v)
	}
}

func TestFirstPartyCloseNeedsReviewer(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "llm"}}
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios_window",
		json.RawMessage(`{"action":"close","window":"w2"}`))
	if spy.tool != "tuios_window" {
		t.Fatalf("next tool = %q, want tuios_window", spy.tool)
	}
	if v.Level != Deny {
		t.Errorf("close verdict = %+v, want the reviewer's deny", v)
	}
}
