package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestAutoInteractiveFastPathRequiresConcreteBoundBuiltin(t *testing.T) {
	call, evidence, _ := browserAutoFixture(t)
	ctx := agentcore.WithToolCallID(context.Background(), "one")
	ctx = agentcore.WithBuiltinReview(ctx, "browser", call.Arguments)
	ctx = agentcore.WithToolReviewEvidence(ctx, call.Name, call.Arguments, evidence)
	if v, ok := autoInteractiveVerdict(ctx, ConsentOpts{}, permissions.Auto, call); !ok || v.Source != "auto-interactive" || !v.Network {
		t.Fatalf("owned browser did not take local path: %+v %t", v, ok)
	}
	for _, extra := range []struct {
		action string
		args   string
	}{
		{"eval", `{"action":"eval","script":"document.title"}`},
		{"upload", `{"action":"upload","files":["README.md"],"selector":"input[type=file]"}`},
	} {
		args := json.RawMessage(extra.args)
		c := agentcore.AgentToolCall{Name: "browser", Arguments: args}
		cctx := agentcore.WithToolCallID(context.Background(), "one")
		cctx = agentcore.WithBuiltinReview(cctx, "browser", args)
		cctx = agentcore.WithToolReviewEvidence(cctx, "browser", args, evidence)
		if v, ok := autoInteractiveVerdict(cctx, ConsentOpts{}, permissions.Auto, c); !ok || v.Source != "auto-interactive" {
			t.Fatalf("%s did not take the local path: %+v %t", extra.action, v, ok)
		}
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		call agentcore.AgentToolCall
		opts ConsentOpts
		mode permissions.Mode
	}{
		{"missing builtin proof", agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, evidence), call, ConsentOpts{}, permissions.Auto},
		{"ask mode", ctx, call, ConsentOpts{}, permissions.Ask},
		{"read-only", ctx, call, ConsentOpts{}, permissions.ReadOnly},
		{"enforce", ctx, call, ConsentOpts{Enforce: true}, permissions.Auto},
		{"changed args", ctx, agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@other"}`)}, ConsentOpts{}, permissions.Auto},
		{"operator required", ctx, agentcore.AgentToolCall{Name: call.Name, Arguments: call.Arguments, RequireOperatorConfirmation: true}, ConsentOpts{}, permissions.Auto},
		{"policy required", ctx, agentcore.AgentToolCall{Name: call.Name, Arguments: call.Arguments, RequirePolicyApproval: true}, ConsentOpts{}, permissions.Auto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := autoInteractiveVerdict(tc.ctx, tc.opts, tc.mode, tc.call); ok {
				t.Fatal("interactive fast path bypassed a boundary")
			}
		})
	}
	external := evidence
	external.RuntimeAttested = false
	ctx = agentcore.WithToolReviewEvidence(ctx, call.Name, call.Arguments, external)
	if _, ok := autoInteractiveVerdict(ctx, ConsentOpts{}, permissions.Auto, call); ok {
		t.Fatal("external browser was automatically authorized")
	}
	externalCall := agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"click","host_control":true}`)}
	externalCtx := agentcore.WithBuiltinReview(context.Background(), "browser", externalCall.Arguments)
	externalCtx = agentcore.WithToolReviewEvidence(externalCtx, "browser", externalCall.Arguments, evidence)
	if _, ok := autoInteractiveVerdict(externalCtx, ConsentOpts{}, permissions.Auto, externalCall); ok {
		t.Fatal("host_control request bypassed authorization")
	}
	resume := agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"resume"}`)}
	resumeCtx := agentcore.WithBuiltinReview(context.Background(), "browser", resume.Arguments)
	resumeCtx = agentcore.WithToolReviewEvidence(resumeCtx, "browser", resume.Arguments, evidence)
	if _, ok := autoInteractiveVerdict(resumeCtx, ConsentOpts{}, permissions.Auto, resume); ok {
		t.Fatal("paused browser recovery bypassed the operator")
	}
}

func TestAutoComputerFastPathDoesNotAcceptSpoofedName(t *testing.T) {
	call := agentcore.AgentToolCall{Name: "computer", Arguments: json.RawMessage(`{"action":"type_text","frame_id":"frame","text":"hello"}`)}
	if _, ok := autoInteractiveVerdict(context.Background(), ConsentOpts{}, permissions.Auto, call); ok {
		t.Fatal("name alone granted automatic control")
	}
	ctx := agentcore.WithBuiltinReview(context.Background(), "computer", call.Arguments)
	if _, ok := autoInteractiveVerdict(ctx, ConsentOpts{}, permissions.Auto, call); !ok {
		t.Fatal("bound built-in computer did not take local path")
	}
}
