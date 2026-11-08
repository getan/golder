package appsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
)

type steeringTestProvider struct {
	calls     atomic.Int32
	requests  chan []string
	releases  []chan struct{}
	firstTool string
}

func (*steeringTestProvider) Name() string { return "steering-test" }
func (*steeringTestProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "steering-test", ID: "test"}}
}

func (p *steeringTestProvider) StreamCompletion(ctx context.Context, req provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	call := int(p.calls.Add(1))
	var history []string
	for _, message := range req.Context.Messages {
		switch m := message.(type) {
		case agentcore.UserMessage:
			history = append(history, "user:"+agentcore.ContentToText(m.Content))
		case agentcore.AssistantMessage:
			history = append(history, "assistant:"+agentcore.ContentToText(m.Content))
		}
	}
	stream := provider.NewAssistantMessageEventStream(1)
	go func() {
		defer stream.Close()
		select {
		case p.requests <- history:
		case <-ctx.Done():
			stream.SetError(ctx.Err())
			return
		}
		if call > len(p.releases) {
			stream.SetError(fmt.Errorf("unexpected provider call %d", call))
			return
		}
		select {
		case <-p.releases[call-1]:
		case <-ctx.Done():
			stream.SetError(ctx.Err())
			return
		}
		answer := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn,
			Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf("answer-%d", call))}}
		if call == 1 && p.firstTool != "" {
			answer.StopReason = agentcore.StopReasonToolUse
			answer.Content = append(answer.Content, agentcore.NewToolCallContent("call-1", p.firstTool, json.RawMessage(`{}`)))
		}
		_ = stream.Emit(ctx, provider.StreamDoneEvent{Message: answer})
	}()
	return stream, nil
}

func newSteeringController(t *testing.T) (*Controller, *steeringTestProvider) {
	t.Helper()
	c, _, _ := newTestController(t)
	p := &steeringTestProvider{requests: make(chan []string, 8), releases: []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
	c.mu.Lock()
	c.live.Provider = p
	c.opt.Provider = p
	c.mu.Unlock()
	return c, p
}

func waitSteeringRequest(t *testing.T, p *steeringTestProvider) []string {
	t.Helper()
	select {
	case req := <-p.requests:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a provider request")
		return nil
	}
}

func waitSteeringEvent(t *testing.T, c *Controller, kind EventKind, text string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-c.Events():
			if event.Kind == kind && (text == "" || event.Item != nil && event.Item.Text == text) {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s %q", kind, text)
		}
	}
}

func TestSteeringQueuesInOrderAndPersists(t *testing.T) {
	c, p := newSteeringController(t)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	if got := waitSteeringRequest(t, p); strings.Join(got, "|") != "user:initial" {
		t.Fatalf("initial request = %v", got)
	}
	for _, message := range []string{"first steer", "second steer"} {
		if err := c.Submit(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := c.Snapshot()
	if !snapshot.Running || strings.Join(snapshot.QueuedMessages, "|") != "first steer|second steer" {
		t.Fatalf("queued state = %+v", snapshot.Status)
	}
	snapshot.QueuedMessages[0] = "changed by caller"
	if got := c.StatusSnapshot().QueuedMessages[0]; got != "first steer" {
		t.Fatalf("snapshot aliases queue: %q", got)
	}
	for _, item := range snapshot.Items {
		if strings.Contains(item.Text, "steer") {
			t.Fatalf("queued input appeared before delivery: %+v", item)
		}
	}
	close(p.releases[0])
	request := waitSteeringRequest(t, p)
	if got := strings.Join(request, "|"); got != "user:initial|assistant:answer-1|user:first steer|user:second steer" {
		t.Fatalf("follow-up request = %s", got)
	}
	waitSteeringEvent(t, c, EventItemAdded, "first steer")
	waitSteeringEvent(t, c, EventItemAdded, "second steer")
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
	var transcript []string
	for _, item := range c.Snapshot().Items {
		if item.Kind == ItemUser || item.Kind == ItemAssistant {
			transcript = append(transcript, string(item.Kind)+":"+item.Text)
		}
	}
	if got := strings.Join(transcript, "|"); got != "user:initial|assistant:answer-1|user:first steer|user:second steer|assistant:answer-2" {
		t.Fatalf("transcript order = %s", got)
	}
	_, entries, err := c.store.LoadEntries(c.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	var persisted []string
	for _, entry := range entries {
		if user, ok := entry.Message.(agentcore.UserMessage); ok {
			persisted = append(persisted, agentcore.ContentToText(user.Content))
		}
	}
	if got := strings.Join(persisted, "|"); got != "initial|first steer|second steer" {
		t.Fatalf("persisted user messages = %s", got)
	}
}

func TestSteeringAfterInterruptStartsNextRun(t *testing.T) {
	c, p := newSteeringController(t)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	if err := c.Submit(context.Background(), "after stop"); err != nil {
		t.Fatal(err)
	}
	c.Cancel()
	request := waitSteeringRequest(t, p)
	if !strings.Contains(strings.Join(request, "|"), "user:after stop") {
		t.Fatalf("queued input was lost after interrupt: %v", request)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
	if pending := c.StatusSnapshot().QueuedMessages; len(pending) != 0 {
		t.Fatalf("queue remained after fallback: %v", pending)
	}
}

func TestSteeringSnapshotsAttachmentContents(t *testing.T) {
	c, p := newSteeringController(t)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("original contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitAttachments(context.Background(), "inspect", []UserAttachment{{Path: path}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(p.releases[0])
	request := strings.Join(waitSteeringRequest(t, p), "|")
	if !strings.Contains(request, "original contents") || strings.Contains(request, "changed contents") {
		t.Fatalf("queued attachment was not snapshotted: %s", request)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

type steeringBlockingTool struct {
	name      string
	started   chan struct{}
	release   chan struct{}
	terminate bool
}

func (t *steeringBlockingTool) Name() string          { return t.name }
func (*steeringBlockingTool) Description() string     { return "test tool" }
func (*steeringBlockingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*steeringBlockingTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t *steeringBlockingTool) Execute(ctx context.Context, _ string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	close(t.started)
	select {
	case <-t.release:
	case <-ctx.Done():
		return agentcore.AgentToolResult{}, ctx.Err()
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("tool complete")}, Terminate: &t.terminate}, nil
}

func installSteeringTestTool(t *testing.T, c *Controller, p *steeringTestProvider, name string, terminate bool) *steeringBlockingTool {
	t.Helper()
	tool := &steeringBlockingTool{name: name, started: make(chan struct{}), release: make(chan struct{}), terminate: terminate}
	p.firstTool = name
	c.mu.Lock()
	c.trust = nil
	c.perms = permissions.New(permissions.FullAccess)
	err := c.reg.Register(tool)
	c.agentCtx.Tools = append(c.agentCtx.Tools, tool)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func waitSteeringTool(t *testing.T, tool *steeringBlockingTool) {
	t.Helper()
	select {
	case <-tool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
}

func TestSteeringDeliveredAfterCurrentTool(t *testing.T) {
	c, p := newSteeringController(t)
	tool := installSteeringTestTool(t, c, p, "steering_test_tool", false)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	close(p.releases[0])
	waitSteeringTool(t, tool)
	if err := c.Submit(context.Background(), "during tool"); err != nil {
		t.Fatal(err)
	}
	if got := c.StatusSnapshot().QueuedMessages; len(got) != 1 || got[0] != "during tool" {
		t.Fatalf("pending input = %v", got)
	}
	close(tool.release)
	request := strings.Join(waitSteeringRequest(t, p), "|")
	if !strings.Contains(request, "user:during tool") {
		t.Fatalf("steering did not reach the next turn: %s", request)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
	toolIndex, userIndex := -1, -1
	for i, item := range c.Snapshot().Items {
		if item.Kind == ItemTool && item.Text == tool.name {
			toolIndex = i
		}
		if item.Kind == ItemUser && item.Text == "during tool" {
			userIndex = i
		}
	}
	if toolIndex < 0 || userIndex <= toolIndex {
		t.Fatalf("steering appeared before its tool: tool=%d user=%d", toolIndex, userIndex)
	}
}

func TestSteeringAfterInterruptedToolStartsNextRun(t *testing.T) {
	c, p := newSteeringController(t)
	tool := installSteeringTestTool(t, c, p, "steering_test_tool", false)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	close(p.releases[0])
	waitSteeringTool(t, tool)
	if err := c.Submit(context.Background(), "after interrupted tool"); err != nil {
		t.Fatal(err)
	}
	c.Cancel()
	request := strings.Join(waitSteeringRequest(t, p), "|")
	if !strings.Contains(request, "user:after interrupted tool") {
		t.Fatalf("input was lost when the tool was interrupted: %s", request)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestSteeringWaitsForComputerHandoff(t *testing.T) {
	c, p := newSteeringController(t)
	tool := installSteeringTestTool(t, c, p, "computer_request", true)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	close(p.releases[0])
	waitSteeringTool(t, tool)
	if err := c.Submit(context.Background(), "queued after handoff"); err != nil {
		t.Fatal(err)
	}
	close(tool.release)
	waitSteeringEvent(t, c, EventRunEnd, "")
	if got := c.StatusSnapshot(); got.Running || len(got.QueuedMessages) != 1 {
		t.Fatalf("handoff did not park queued input: %+v", got)
	}
	if err := c.StartQueued(context.Background()); err == nil {
		t.Fatal("queued message started before the computer handoff was resolved")
	}
	if err := c.NewSession(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("session switched while input was queued: %v", err)
	}
	c.ResetComputerRequest()
	if err := c.StartQueued(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := strings.Join(waitSteeringRequest(t, p), "|")
	if !strings.Contains(request, "user:queued after handoff") {
		t.Fatalf("queued input lost after handoff: %s", request)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestSteeringStaysWithBackgroundSession(t *testing.T) {
	hub, first, p, project := setupConcurrentHub(t)
	p.release["alpha-follow"] = make(chan struct{})
	if err := first.Submit(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "alpha")
	if err := first.Submit(context.Background(), "alpha-follow"); err != nil {
		t.Fatal(err)
	}
	second, err := hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Submit(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "beta")
	close(p.release["alpha"])
	waitSessionProvider(t, p, "alpha-follow")
	if got := second.StatusSnapshot().QueuedMessages; len(got) != 0 {
		t.Fatalf("queue leaked to another session: %v", got)
	}
	close(p.release["alpha-follow"])
	waitHubEvent(t, hub, first.SessionID(), EventRunEnd)
	for _, item := range second.Snapshot().Items {
		if strings.Contains(item.Text, "alpha-follow") {
			t.Fatalf("queued message leaked into another transcript: %+v", item)
		}
	}
	close(p.release["beta"])
	waitHubEvent(t, hub, second.SessionID(), EventRunEnd)
}

func TestQueuedBackgroundSessionCannotBeDeleted(t *testing.T) {
	hub, first, _, project := setupConcurrentHub(t)
	first.mu.Lock()
	first.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("pending")}, "pending")
	first.steerHandoff = true
	first.mu.Unlock()
	if _, err := hub.NewSession(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if err := hub.DeleteSession(context.Background(), first.SessionID()); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("queued background session was removed: %v", err)
	}
	if got := first.StatusSnapshot().QueuedMessages; len(got) != 1 || got[0] != "pending" {
		t.Fatalf("queued input changed: %v", got)
	}
}

func TestSteeringLateInputStartsFallbackRun(t *testing.T) {
	c, p := newSteeringController(t)
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running, c.status.Running, c.cancel = true, true, cancel
	c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("late input")}, "late input")
	c.mu.Unlock()
	nextCtx, nextID, prompt, ok := c.finishRun(ctx, nil, nil, false)
	if !ok || prompt.display != "late input" || !c.IsRunning() {
		t.Fatalf("late input was not reserved for the next run: %+v %t", prompt, ok)
	}
	if !c.prepareQueuedPrompt(nextCtx, prompt) {
		t.Fatal("could not prepare the queued prompt")
	}
	c.runWG.Add(1)
	go c.run(nextCtx, nextID, prompt.display)
	if got := strings.Join(waitSteeringRequest(t, p), "|"); got != "user:late input" {
		t.Fatalf("fallback request = %s", got)
	}
	close(p.releases[0])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestInterruptedComputerHandoffLeavesQueueAvailable(t *testing.T) {
	c, p := newSteeringController(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.mu.Lock()
	c.running, c.status.Running, c.cancel = true, true, cancel
	c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("pending")}, "pending")
	c.mu.Unlock()
	_, _, _, started := c.finishRun(ctx, nil, nil, true)
	if started || c.IsRunning() || len(c.StatusSnapshot().QueuedMessages) != 1 {
		t.Fatal("interrupted handoff lost input or continued before settling")
	}
	waitSteeringEvent(t, c, EventRunEnd, "")
	c.ResetComputerRequest()
	if err := c.StartQueued(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(waitSteeringRequest(t, p), "|"); got != "user:pending" {
		t.Fatalf("request after interrupted handoff = %s", got)
	}
	close(p.releases[0])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestNewInputDoesNotOvertakeParkedQueue(t *testing.T) {
	c, p := newSteeringController(t)
	c.mu.Lock()
	c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("first")}, "first")
	c.mu.Unlock()
	if err := c.Submit(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(waitSteeringRequest(t, p), "|"); got != "user:first" {
		t.Fatalf("first request overtook queued input: %s", got)
	}
	close(p.releases[0])
	if got := strings.Join(waitSteeringRequest(t, p), "|"); got != "user:first|assistant:answer-1|user:second" {
		t.Fatalf("second request = %s", got)
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestSteeringDrainUsesAtomicBatch(t *testing.T) {
	c, _ := newSteeringController(t)
	c.mu.Lock()
	for _, text := range []string{"first", "second"} {
		c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent(text)}, text)
	}
	c.mu.Unlock()
	stream := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](0)
	result := make(chan []agentcore.AgentMessage, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { result <- c.pullSteering(ctx, stream) }()
	select {
	case <-stream.Events():
		c.showDeliveredSteer()
	case <-ctx.Done():
		t.Fatal("first queued message was not delivered")
	}
	c.mu.Lock()
	c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("third")}, "third")
	c.mu.Unlock()
	select {
	case <-stream.Events():
		c.showDeliveredSteer()
	case <-ctx.Done():
		t.Fatal("second queued message was not delivered")
	}
	select {
	case messages := <-result:
		if len(messages) != 2 {
			t.Fatalf("batch contained %d messages, want 2", len(messages))
		}
	case <-ctx.Done():
		t.Fatal("drain did not finish")
	}
	if got := c.StatusSnapshot().QueuedMessages; len(got) != 1 || got[0] != "third" {
		t.Fatalf("new input did not wait for the next seam: %v", got)
	}
}

func TestCanceledSteeringKeepsQueuedInput(t *testing.T) {
	c, _ := newSteeringController(t)
	c.mu.Lock()
	for _, text := range []string{"first", "second"} {
		c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent(text)}, text)
	}
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := agentcore.NewEventStream[agentcore.AgentEvent, []agentcore.AgentMessage](0)
	if messages := c.pullSteering(ctx, stream); len(messages) != 0 {
		t.Fatalf("canceled pull delivered %d messages", len(messages))
	}
	if err := c.StartQueued(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start = %v", err)
	}
	if got := strings.Join(c.StatusSnapshot().QueuedMessages, "|"); got != "first|second" {
		t.Fatalf("cancellation changed queued input: %s", got)
	}
}
