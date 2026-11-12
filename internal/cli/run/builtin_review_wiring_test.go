package run

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/provider"
)

func TestNewReviewerSettlesAttestedBookkeepingBeforeBothRoutineGraders(t *testing.T) {
	t.Setenv("GOLDER_JEV", "on")
	for _, configured := range []bool{false, true} {
		name := "local-routine"
		var creds *provider.CredentialStore
		if configured {
			name = "decision-model-configured"
			creds = provider.NewCredentialStore(nil)
			creds.SetOverride("typesafe", "test-only-not-used")
		}
		t.Run(name, func(t *testing.T) {
			reviewer := NewReviewer("m", "opencode-go", nil, creds, "", t.TempDir(), true)
			for _, tc := range []struct{ tool, args string }{
				{"todo", `{"todos":[]}`},
				{"get_context_remaining", `{}`},
				{"schedule_list", `{}`},
				{"write_stdin", `{"bash_id":"bash_1","chars":""}`},
			} {
				raw := json.RawMessage(tc.args)
				ctx := agentcore.WithToolCallID(context.Background(), "call")
				ctx = agentcore.WithBuiltinReview(ctx, tc.tool, raw)
				if got := reviewer.Classify(ctx, tc.tool, raw); got.Level != judge.Allow || got.Source != judge.LocalSource || got.Failed {
					t.Fatalf("%s was not settled locally: %+v", tc.tool, got)
				}
			}
		})
	}
}
