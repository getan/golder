package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestFirstPartyFastPathIsLimitedToAttestedBookkeepingAndPolls(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		local     bool
	}{
		{"todo", `{"todos":[]}`, true},
		{"get_context_remaining", `{}`, true},
		{"schedule_list", `{}`, true},
		{"write_stdin", `{"bash_id":"bash_1"}`, true},
		{"write_stdin", `{"bash_id":"bash_1","chars":"\u0003"}`, false},
		{"memory_search", `{"query":"private"}`, false},
		{"new_context", `{}`, false},
		{"schedule_create", `{"prompt":"future task","every_seconds":300}`, false},
		{"schedule_delete", `{"id":"sched-1"}`, false},
		{"task", `{"prompt":"do work"}`, false},
	} {
		t.Run(tc.name+tc.raw, func(t *testing.T) {
			args := json.RawMessage(tc.raw)
			ctx := agentcore.WithToolCallID(context.Background(), "id")
			ctx = agentcore.WithBuiltinReview(ctx, tc.name, args)
			spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "reviewer"}}
			got := (FirstParty{Next: spy}).Classify(ctx, tc.name, args)
			if tc.local {
				if spy.tool != "" || got.Level != Allow || got.Source != LocalSource {
					t.Fatalf("not classified locally: verdict=%+v next=%q", got, spy.tool)
				}
			} else if spy.tool != tc.name || got.Level != Deny {
				t.Fatalf("unlisted action skipped the reviewer: verdict=%+v next=%q", got, spy.tool)
			}
		})
	}
}

func TestFirstPartyDoesNotTrustNameOrStaleArguments(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny, Source: "reviewer"}}
	args := json.RawMessage(`{"todos":[]}`)
	ctx := agentcore.WithToolCallID(context.Background(), "id")
	if got := (FirstParty{Next: spy}).Classify(ctx, "todo", args); spy.tool != "todo" || got.Level != Deny {
		t.Fatalf("name alone bypassed reviewer: %+v", got)
	}
	spy.tool = ""
	ctx = agentcore.WithBuiltinReview(ctx, "todo", args)
	if got := (FirstParty{Next: spy}).Classify(ctx, "todo", json.RawMessage(`{"todos":[{"content":"changed"}]}`)); spy.tool != "todo" || got.Level != Deny {
		t.Fatalf("changed arguments bypassed reviewer: %+v", got)
	}
}
