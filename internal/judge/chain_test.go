package judge

// Tests for the chain: the static floor, then the reviewer, and nothing between
// them.
//
// The chain used to carry two exemptions — a provably read-only shell command,
// and a set of tools with no side effects. Both are gone by decision: every
// call is graded. What these tests pin is that nothing reaches the reviewer
// zero times, while the floor still decides what it owns.

import (
	"context"
	"encoding/json"
	"testing"
)

// reviewerSpy counts how many calls actually reached the reviewer.
type reviewerSpy struct {
	calls int
	v     Verdict
}

func (s *reviewerSpy) Classify(context.Context, string, json.RawMessage) Verdict {
	s.calls++
	return s.v
}

// TestChainReviewsEveryTool is the property the design now has: no tool, and no
// shape of call, reaches the reviewer zero times. The list is deliberately the
// calls that used to be exempt — read-only shell commands and side-effect-free
// tools — alongside the mutating ones, so an exemption cannot come back without
// failing here.
func TestChainReviewsEveryTool(t *testing.T) {
	bashCalls := []string{
		"ls -la",
		`rg -n "explore" -i codex-rs | head -50`,
		`cat internal/main.go 2>/dev/null`,
		`find . -name "*.go"`,
		`go test ./...`,
		`rm -rf ./build`,
	}
	for _, cmd := range bashCalls {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy}.Classify(context.Background(), "bash", bashArgs(cmd))
		if spy.calls != 1 {
			t.Errorf("bash %q reached the reviewer %d times, want 1", cmd, spy.calls)
		}
		if v.Source != "llm" {
			t.Errorf("bash %q verdict = %+v, want the reviewer's", cmd, v)
		}
	}

	// Every other built-in tool, with the arguments that make it name a path
	// (for the boundary gate) or a URL (for the network reads).
	native := []struct {
		tool, args string
	}{
		{"read", `{"path":"a.go"}`},
		{"view_image", `{"path":"a.png"}`},
		{"grep", `{"pattern":"x","path":"."}`},
		{"find", `{"glob":"*.go"}`},
		{"ls", `{"path":"."}`},
		{"webfetch", `{"url":"https://example.com"}`},
		{"websearch", `{"query":"golder"}`},
		{"todo", `{"todos":[]}`},
		{"memory_search", `{"query":"x"}`},
		{"schedule_list", `{}`},
		{"schedule_create", `{"every_seconds":300,"prompt":"x"}`},
		{"schedule_delete", `{"id":"sched-1"}`},
		{"get_context_remaining", `{}`},
		{"new_context", `{}`},
		{"write_stdin", `{"bash_id":"b1"}`},
		{"goal_complete", `{"summary":"done"}`},
		{"goal_blocked", `{"reason":"stuck"}`},
		{"apply_patch", `{"patch":"*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch\n"}`},
	}
	for _, c := range native {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy}.Classify(context.Background(), c.tool, json.RawMessage(c.args))
		if spy.calls != 1 {
			t.Errorf("%s reached the reviewer %d times, want 1", c.tool, spy.calls)
		}
		if v.Source != "llm" {
			t.Errorf("%s verdict = %+v, want the reviewer's", c.tool, v)
		}
	}
}

// TestChainKeepsTheFloorInFront pins the ordering that still matters: the floor
// is the only layer that denies without asking a model, so a credential read is
// decided there and the reviewer never sees it.
func TestChainKeepsTheFloorInFront(t *testing.T) {
	for _, cmd := range []string{
		`cat ~/.zshrc`,
		`cat ~/.ssh/id_rsa`,
		`ls ~/.aws`,
		`grep -rn x ~/.ssh`,
	} {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy}.Classify(context.Background(), "bash", bashArgs(cmd))
		if v.Level != Deny {
			t.Errorf("%q verdict = %+v, want Deny from the static floor", cmd, v)
		}
		if spy.calls != 0 {
			t.Errorf("%q reached the reviewer, want the floor to have decided", cmd)
		}
		if v.Source != "static" {
			t.Errorf("%q was decided by %q, want the static floor", cmd, v.Source)
		}
	}
	// The static credential floor remains authoritative regardless of how the
	// default sandbox would constrain this call at execution time.
}

// TestChainSendsEscalationRequestsToTheReviewer keeps the containment contract:
// a call asking to run outside the sandbox is graded like any other, so the
// request is weighed rather than swallowed.
func TestChainSendsEscalationRequestsToTheReviewer(t *testing.T) {
	escalated := func(cmd string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{
			"command":             cmd,
			"sandbox_permissions": "require_escalated",
			"justification":       "the sandbox hides a tree I need",
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, cmd := range []string{"ls -la .", `rg -n TODO internal`, "cat go.mod"} {
		spy := &reviewerSpy{v: Verdict{Level: Contain, Source: "llm"}}
		v := Chain{Inner: spy}.Classify(context.Background(), "bash", escalated(cmd))
		if spy.calls != 1 {
			t.Errorf("%q with require_escalated reached the reviewer %d times, want 1", cmd, spy.calls)
		}
		if v.Source != "llm" {
			t.Errorf("%q verdict = %+v, want the reviewer's", cmd, v)
		}
	}
}
