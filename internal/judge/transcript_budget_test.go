package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestTranscriptKeepsEarlierUserIntentAheadOfRecentToolOutput(t *testing.T) {
	user := func(text string) agentcore.Message {
		return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
	}
	msgs := agentcore.MessageList{
		user("请部署到指定服务器并重启 API 服务"),
		user("先检查构建"), user("继续"), user("再检查状态"),
		user("现在推送代码"),
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("a", 2000))}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "bash", Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("b", 3000))}},
	}
	got := transcriptSection(msgs)
	if !strings.Contains(got, "请部署到指定服务器并重启 API 服务") {
		t.Fatalf("earlier user authorization was omitted: %q", got)
	}
	if strings.Index(got, "请部署到指定服务器") > strings.Index(got, "现在推送代码") {
		t.Fatal("user messages must remain chronological")
	}
}

func TestTranscriptPrioritizesUsersWhenBudgetIsTight(t *testing.T) {
	var msgs agentcore.MessageList
	for i := 0; i < 6; i++ {
		msgs = append(msgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("u", 990))}})
	}
	msgs = append(msgs, agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "bash", Content: agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("t", 1000))}})
	got := transcriptSection(msgs)
	if count := strings.Count(got, "user: "); count != 6 {
		t.Fatalf("retained %d of 6 user messages", count)
	}
}

func TestReviewerReceivesEarlierDeploymentAuthorization(t *testing.T) {
	var msgs agentcore.MessageList
	for _, text := range []string{
		"请部署到指定服务器并重启 API 服务", "先检查构建", "继续", "再检查状态", "现在推送代码",
	} {
		msgs = append(msgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}})
	}
	ctx := agentcore.WithMessageSnapshot(context.Background(), msgs)
	stub := &reviewStub{answer: `{"level":"confirm","risk":"medium","authorization":"high","rationale":"用户要求部署","network":"allow"}`}
	judge := NewLLMJudge(stub.fn, t.TempDir(), true)
	verdict := judge.Classify(ctx, "tuios", json.RawMessage(`{"action":"run","command":"restart-api","network":"on"}`))
	if verdict.Failed || verdict.Level != Confirm || !verdict.Network {
		t.Fatalf("verdict = %+v", verdict)
	}
	if !strings.Contains(stub.user, "请部署到指定服务器并重启 API 服务") {
		t.Fatalf("deployment authorization missing from reviewer state: %s", stub.user)
	}
}
