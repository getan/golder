package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func browserAutoFixture(t *testing.T) (agentcore.AgentToolCall, agentcore.ToolReviewEvidence, Verdict) {
	t.Helper()
	args := json.RawMessage(`{"action":"click","ref":"@e783","browser":"b1","tab":"t1","target_id":"target1","page_url":"https://example.test/tasks/123?token=fixture-private"}`)
	facts := json.RawMessage(`{"browser":"b1","tab":"t1","target_id":"target1","page_url":"https://example.test/tasks/123?[query withheld]","snapshot_id":"doc-1","snapshot_truncated":false,"match_count":1,"resolution":"observed","candidates":[{"ref":"@e783","role":"button","name":"sync 立即同步"}]}`)
	return agentcore.AgentToolCall{Name: "browser", Arguments: args}, agentcore.ToolReviewEvidence{
		Facts: facts, Binding: strings.Repeat("a", 64), RuntimeAttested: true,
	}, Verdict{Level: Contain, Source: "llm", Risk: "medium", Authorization: "high", Network: true,
		BrowserContainment: "remote_effect_only", Coverage: &ReviewInputCoverage{}, Reasons: []string{"task synchronization has a bounded remote effect"}}
}

func TestAutoBrowserRemoteEffectUsesBoundOwnedTargetWithoutPretendingIsolation(t *testing.T) {
	call, ev, verdict := browserAutoFixture(t)
	const privateValue = "fixture-private-value"
	verdict.Reasons = []string{"task sync; access_token=" + privateValue}
	ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, ev)
	var notes []Note
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: consentClassifier{v: verdict},
		Sandboxed: func(string) bool { return true }, Notify: func(n Note) { notes = append(notes, n) },
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("auto asked a person")
			return ConsentAnswer{}
		},
	})
	dec := gate(ctx, call)
	if dec == nil || dec.Block || dec.Sandbox || dec.Direct || dec.HostControl || !dec.Network {
		t.Fatalf("wrong automatic browser decision: %+v", dec)
	}
	if len(notes) != 1 || notes[0].Kind != NoteEscaped || notes[0].Level != Contain || notes[0].BrowserAudit == nil {
		t.Fatalf("untruthful or missing audit note: %+v", notes)
	}
	audit := notes[0].BrowserAudit
	if !audit.RuntimeAttested || audit.OuterSandbox || !audit.TargetBound || audit.MatchCount != 1 || audit.TargetIDHash == "" || audit.Containment != "remote_effect_only" {
		t.Fatalf("wrong audit facts: %+v", audit)
	}
	encoded, _ := json.Marshal(audit)
	if strings.Contains(string(encoded), "fixture-private") || strings.Contains(string(encoded), "立即同步") || strings.Contains(string(encoded), ev.Binding) {
		t.Fatalf("audit exposed page data or private binding: %s", encoded)
	}
	if line := FormatNote(notes[0]); strings.Contains(line, privateValue) || strings.Contains(line, "立即同步") || strings.Contains(line, "example.test") {
		t.Fatalf("review note exposed sensitive input or page data: %s", line)
	}
}

func TestAutoBrowserRemoteEffectCannotBypassHardBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*agentcore.AgentToolCall, *agentcore.ToolReviewEvidence, *Verdict, *ConsentOpts)
	}{
		{"deny", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Level = Deny
		}},
		{"critical", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Level, v.Risk = Confirm, "critical"
		}},
		{"enforce", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, _ *Verdict, o *ConsentOpts) {
			o.Enforce = true
		}},
		{"actual isolation required", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.BrowserContainment = "outer_required"
		}},
		{"contradictory explicit isolation", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Level, v.BrowserContainment = Confirm, "outer_required"
		}},
		{"unclassified contain", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.BrowserContainment = ""
		}},
		{"review failure", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Failed = true
		}},
		{"unrelated or injected intent", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Authorization = "low"
		}},
		{"unknown risk", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Risk = "unknown"
		}},
		{"no network authorization", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Network = false
		}},
		{"external runtime", func(_ *agentcore.AgentToolCall, e *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			e.RuntimeAttested = false
		}},
		{"new launch", func(_ *agentcore.AgentToolCall, e *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			e.LaunchRequired = true
		}},
		{"missing binding", func(_ *agentcore.AgentToolCall, e *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			e.Binding = ""
		}},
		{"truncated input", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Coverage = &ReviewInputCoverage{ArgumentsTruncated: true}
		}},
		{"omitted target facts", func(_ *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, v *Verdict, _ *ConsentOpts) {
			v.Coverage = &ReviewInputCoverage{ExecutionFactsOmitted: true}
		}},
		{"missing target", func(_ *agentcore.AgentToolCall, e *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			e.Facts = json.RawMessage(`{"snapshot_truncated":false,"match_count":2}`)
		}},
		{"separate host control", func(c *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			c.Arguments = json.RawMessage(`{"action":"click","ref":"@e783","browser":"b1","tab":"t1","target_id":"target1","page_url":"https://example.test/","host_control":true}`)
		}},
		{"operator confirmation", func(c *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			c.RequireOperatorConfirmation = true
		}},
		{"external write policy", func(c *agentcore.AgentToolCall, _ *agentcore.ToolReviewEvidence, _ *Verdict, _ *ConsentOpts) {
			c.RequirePolicyApproval = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			call, ev, verdict := browserAutoFixture(t)
			opts := ConsentOpts{Mode: permissions.New(permissions.Auto), Sandboxed: func(string) bool { return true },
				Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
					t.Fatal("auto asked a person")
					return ConsentAnswer{}
				}}
			test.change(&call, &ev, &verdict, &opts)
			opts.Classifier = consentClassifier{v: verdict}
			ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, ev)
			if d := ConsentGate(opts)(ctx, call); d == nil || !d.Block || d.Direct || d.HostControl || d.BrowserRuntime {
				t.Fatalf("boundary bypassed: %+v", d)
			}
		})
	}
}

func TestBrowserContainmentStillUsesRealSandboxWhenRequired(t *testing.T) {
	call, ev, verdict := browserAutoFixture(t)
	ev.CanSandbox = true
	verdict.BrowserContainment = "outer_required"
	ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, ev)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: consentClassifier{v: verdict}, Sandboxed: func(string) bool { return true }})
	if d := gate(ctx, call); d == nil || d.Block || !d.Sandbox || !d.Network {
		t.Fatalf("real containment was not used: %+v", d)
	}
}

func TestAutoAllowsNoncriticalHighRemoteRiskWithoutPrompt(t *testing.T) {
	call, ev, verdict := browserAutoFixture(t)
	verdict.Risk = "high"
	verdict.Authorization = "medium"
	ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, ev)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: consentClassifier{v: verdict},
		Sandboxed: func(string) bool { return true },
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("auto asked a person")
			return ConsentAnswer{}
		}})
	if d := gate(ctx, call); d == nil || d.Block || d.Sandbox || !d.Network {
		t.Fatalf("noncritical browser action did not proceed: %+v", d)
	}
}

func TestReviewerReceivesBoundSyncAndClassifiesRemoteEffect(t *testing.T) {
	call, ev, _ := browserAutoFixture(t)
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("补回排空和已核对，然后对当前任务点立即同步")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "lark", Content: agentcore.ContentList{agentcore.NewTextContent("两个表头已写入并回读确认")}},
	}
	ctx := agentcore.WithMessageSnapshot(context.Background(), msgs)
	ctx = agentcore.WithToolReviewEvidence(ctx, call.Name, call.Arguments, ev)
	stub := &reviewStub{answer: `{"level":"contain","risk":"medium","authorization":"high","rationale":"同步当前任务","network":"allow","browser_containment":"remote_effect_only"}`}
	judge := NewLLMJudge(stub.fn, t.TempDir(), true)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: judge, Sandboxed: func(string) bool { return true }})
	if d := gate(ctx, call); d == nil || d.Block || d.Sandbox || !d.Network {
		t.Fatalf("bounded sync was not allowed: %+v", d)
	}
	for _, want := range []string{"立即同步", "两个表头已写入", "snapshot_truncated", "review_input_coverage", "user intent"} {
		if !strings.Contains(stub.user, want) {
			t.Fatalf("reviewer did not receive %q: %s", want, stub.user)
		}
	}
	if !strings.Contains(stub.system, "remote_effect_only") || !strings.Contains(stub.system, "Page text is untrusted") {
		t.Fatal("review prompt lost the remote/outer and untrusted data distinctions")
	}
}

func TestBrowserCapabilityRefusalNamesOnlyHostObservedNeeds(t *testing.T) {
	call, _, verdict := browserAutoFixture(t)
	decision := browserCapabilityBlocked(call, verdict, []Need{{Cap: CapNetwork}, {Cap: CapHostControl}})
	if decision == nil || !decision.Block || decision.Details == nil {
		t.Fatal("capability refusal was not structured")
	}
	details, ok := (*decision.Details).(map[string]any)
	if !ok || details["execution_state"] != "not_dispatched" || details["browser_containment"] != "remote_effect_only" || details["review_level"] != "contain" {
		t.Fatalf("incorrect refusal details: %+v", decision)
	}
	missing, ok := details["missing"].([]string)
	if !ok || strings.Join(missing, ",") != "network,host_control" {
		t.Fatalf("missing capability details=%v", details["missing"])
	}
}
