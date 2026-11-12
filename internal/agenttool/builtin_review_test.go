package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

type builtinReviewerSpy struct{ calls int }

func (s *builtinReviewerSpy) Classify(context.Context, string, json.RawMessage) judge.Verdict {
	s.calls++
	return judge.Verdict{Level: judge.Deny, Source: "reviewer"}
}

func TestBookkeepingBuiltinsUseLocalClassifierThroughExecutor(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.Auto, permissions.Ask, permissions.ReadOnly} {
		for _, tc := range []struct {
			name string
			tool agentcore.AgentTool
			args string
		}{
			{"todo", &TodoTool{Store: NewTodoStore()}, `{"todos":[{"content":"one","status":"in_progress"}]}`},
			{"get_context_remaining", &GetContextRemainingTool{}, `{}`},
			{"schedule_list", ScheduleTools(NewSchedule())[1], `{}`},
		} {
			t.Run(mode.String()+"/"+tc.name, func(t *testing.T) {
				spy := &builtinReviewerSpy{}
				asked := 0
				cfg := newExecCfg(t, tc.tool)
				cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{
					Mode: permissions.New(mode), Cwd: t.TempDir(),
					Classifier: judge.Chain{Inner: judge.FirstParty{Next: spy}},
					Confirm: func(context.Context, judge.ConsentRequest) judge.ConsentAnswer {
						asked++
						return judge.ConsentAnswer{}
					},
				})
				msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "one", Name: tc.name, Arguments: json.RawMessage(tc.args)}, nil)
				if msg.IsError || spy.calls != 0 || asked != 0 {
					t.Fatalf("builtin did not take local path: result=%+v reviewer=%d prompts=%d", msg, spy.calls, asked)
				}
			})
		}
	}
}

func TestBookkeepingNameDoesNotLetCustomToolBypassReview(t *testing.T) {
	spy, ran := &builtinReviewerSpy{}, false
	tool := execTool{name: "todo", schema: `{"type":"object"}`, run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		ran = true
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{
		Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(),
		Classifier: judge.Chain{Inner: judge.FirstParty{Next: spy}},
	})
	args := json.RawMessage(`{"todos":[]}`)
	// A nested call may inherit an older matching context. The executor must
	// clear that proof even when the name, call ID and arguments match.
	ctx := agentcore.WithToolCallID(context.Background(), "same")
	ctx = agentcore.WithBuiltinReview(ctx, "todo", args)
	msg, _ := executeToolCall(ctx, cfg, agentcore.AgentToolCall{ID: "same", Name: "todo", Arguments: args}, nil)
	if ran || !msg.IsError || spy.calls != 1 {
		t.Fatalf("custom tool bypassed review: ran=%t result=%+v reviewer=%d", ran, msg, spy.calls)
	}
}

func TestBookkeepingSchemaFailureNeverReachesFastPath(t *testing.T) {
	spy := &builtinReviewerSpy{}
	cfg := newExecCfg(t, &TodoTool{Store: NewTodoStore()})
	cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		spy.calls++
		return nil
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "bad", Name: "todo", Arguments: json.RawMessage(`{"todos":"not-an-array"}`)}, nil)
	if !msg.IsError || spy.calls != 0 {
		t.Fatalf("invalid args reached the gate or executed: result=%+v gate=%d", msg, spy.calls)
	}
}

func TestOnlyEmptyWriteStdinPollUsesLocalClassifier(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		local      bool
	}{
		{"omitted", `{"bash_id":"bash_1"}`, true},
		{"empty", `{"bash_id":"bash_1","chars":"","yield_time_ms":1000}`, true},
		{"interrupt", `{"bash_id":"bash_1","chars":"\u0003"}`, false},
		{"interrupt alias", `{"bash_id":"bash_1","chars":"^C"}`, false},
		{"whitespace input", `{"bash_id":"bash_1","chars":" "}`, false},
		{"interactive input", `{"bash_id":"bash_1","chars":"yes\n"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &builtinReviewerSpy{}
			var verdict judge.Verdict
			cfg := newExecCfg(t, &WriteStdinTool{})
			cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				verdict = (judge.FirstParty{Next: spy}).Classify(ctx, call.Name, call.Arguments)
				return &agentcore.BeforeToolCallDecision{Block: true}
			}
			_, _ = executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "poll", Name: "write_stdin", Arguments: json.RawMessage(tc.args)}, nil)
			if tc.local {
				if spy.calls != 0 || verdict.Source != judge.LocalSource || verdict.Level != judge.Allow {
					t.Fatalf("empty poll reached reviewer: verdict=%+v calls=%d", verdict, spy.calls)
				}
			} else if spy.calls != 1 || verdict.Level != judge.Deny {
				t.Fatalf("input bypassed review: verdict=%+v calls=%d", verdict, spy.calls)
			}
		})
	}
}

func TestWriteStdinPollDoesNotChangeReadOnlyMode(t *testing.T) {
	spy := &builtinReviewerSpy{}
	cfg := newExecCfg(t, &WriteStdinTool{})
	cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{
		Mode: permissions.New(permissions.ReadOnly), Cwd: t.TempDir(),
		Classifier: judge.Chain{Inner: judge.FirstParty{Next: spy}},
	})
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "poll", Name: "write_stdin", Arguments: json.RawMessage(`{"bash_id":"bash_1"}`)}, nil)
	if !msg.IsError || spy.calls != 0 {
		t.Fatalf("read-only policy was changed: result=%+v reviewer=%d", msg, spy.calls)
	}
}
