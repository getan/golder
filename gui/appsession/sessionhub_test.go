package appsession

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

type concurrentSessionProvider struct {
	started chan string
	release map[string]chan struct{}
}

func (*concurrentSessionProvider) Name() string { return "fake" }
func (*concurrentSessionProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "fake", ID: "fake"}}
}
func (p *concurrentSessionProvider) StreamCompletion(ctx context.Context, req provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	prompt := ""
	for _, message := range req.Context.Messages {
		if user, ok := message.(agentcore.UserMessage); ok {
			prompt = agentcore.ContentToText(user.Content)
		}
	}
	stream := provider.NewAssistantMessageEventStream(4)
	go func() {
		defer stream.Close()
		p.started <- prompt
		select {
		case <-ctx.Done():
			return
		case <-p.release[prompt]:
		}
		answer := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn,
			Content: agentcore.ContentList{agentcore.NewTextContent("reply to " + prompt)}}
		if prompt == "fail" {
			answer.StopReason = agentcore.StopReasonError
			answer.ErrorMessage = "provider failed"
		}
		_ = stream.Emit(ctx, provider.StreamDoneEvent{Message: answer})
	}()
	return stream, nil
}

func setupConcurrentHub(t *testing.T) (*SessionHub, *Controller, *concurrentSessionProvider, string) {
	t.Helper()
	initial, _, project := newTestController(t)
	p := &concurrentSessionProvider{started: make(chan string, 4), release: map[string]chan struct{}{"alpha": make(chan struct{}), "beta": make(chan struct{}), "fail": make(chan struct{})}}
	initial.mu.Lock()
	initial.live.Provider = p
	initial.opt.Provider = p
	initial.factory = func(_ context.Context, cwd string, setup ProjectSetup) (run.Env, error) {
		return run.Env{Cwd: cwd, ProviderName: setup.ProviderName, Provider: p,
			Credentials: provider.NewCredentialStore(nil), SysPrompt: "project " + cwd,
			Prompt: runtime.PromptInputs{WorkingDir: cwd}, Permissions: permissions.New(permissions.Auto),
			ReviewNotes: run.NewReviewNotes(), Budget: contextbudget.New()}, nil
	}
	initial.mu.Unlock()
	hub := NewSessionHub(initial)
	t.Cleanup(func() { _ = hub.Close() })
	return hub, initial, p, project
}

func waitHubEvent(t *testing.T, hub *SessionHub, sessionID string, kind EventKind) Event {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case message := <-hub.Events():
			if message.Event.Session == sessionID && message.Event.Kind == kind {
				return message.Event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s in %s", kind, sessionID)
		}
	}
}

func waitSessionProvider(t *testing.T, p *concurrentSessionProvider, want string) {
	t.Helper()
	select {
	case got := <-p.started:
		if got != want {
			t.Fatalf("provider request = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("provider did not receive %q", want)
	}
}

func TestSessionHubRunsAndPersistsConversationsIndependently(t *testing.T) {
	hub, first, p, project := setupConcurrentHub(t)
	ctx := context.Background()
	if err := first.Submit(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "alpha")
	second, err := hub.NewSession(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.SessionID() == second.SessionID() || !first.IsRunning() || first.perms == second.perms || first.reg == second.reg {
		t.Fatal("sessions did not retain independent live runtime state")
	}
	if err := second.Submit(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "beta")
	if selected, err := hub.Resume(ctx, first.SessionID()); err != nil || selected != first || !first.IsRunning() || !second.IsRunning() {
		t.Fatalf("cannot return to a running session: %v", err)
	}
	if _, err := hub.Resume(ctx, second.SessionID()); err != nil {
		t.Fatal(err)
	}
	if err := hub.DeleteSession(ctx, first.SessionID()); err == nil || !first.IsRunning() {
		t.Fatal("running background session was removed or canceled")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := hub.Resume(canceled, first.SessionID()); !errors.Is(err, context.Canceled) || hub.Current() != second {
		t.Fatalf("canceled switch changed the current session: %v", err)
	}
	if _, err := hub.Resume(ctx, "../outside"); err == nil || hub.Current() != second {
		t.Fatal("unsafe session id was accepted or changed the current session")
	}
	rows, err := hub.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if (row.ID == first.SessionID() || row.ID == second.SessionID()) && !row.Running {
			t.Fatalf("running session missing its status: %+v", row)
		}
		if row.ID == first.SessionID() && row.Title != "alpha" || row.ID == second.SessionID() && row.Title != "beta" {
			t.Fatalf("live session has the wrong title: %+v", row)
		}
	}
	close(p.release["alpha"])
	waitHubEvent(t, hub, first.SessionID(), EventRunEnd)
	rows, err = hub.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var firstRow SessionSummary
	for _, row := range rows {
		if row.ID == first.SessionID() {
			firstRow = row
		}
	}
	if !firstRow.Unread || firstRow.Running || !second.IsRunning() {
		t.Fatalf("background completion was not reflected: %+v", firstRow)
	}
	foundAlpha := false
	for _, item := range first.Snapshot().Items {
		if strings.Contains(item.Text, "beta") {
			t.Fatalf("second conversation leaked into first: %+v", item)
		}
		foundAlpha = foundAlpha || strings.Contains(item.Text, "reply to alpha")
	}
	if !foundAlpha {
		t.Fatal("background reply did not reach its original transcript")
	}
	for _, item := range second.Snapshot().Items {
		if strings.Contains(item.Text, "alpha") {
			t.Fatalf("first conversation leaked into second: %+v", item)
		}
	}
	selected, err := hub.Resume(ctx, first.SessionID())
	if err != nil || selected != first || !second.IsRunning() {
		t.Fatalf("return to live session = %p, %v", selected, err)
	}
	rows, err = hub.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == first.SessionID() && row.Unread {
			t.Fatal("viewing a session did not clear its completion marker")
		}
	}
	close(p.release["beta"])
	waitHubEvent(t, hub, second.SessionID(), EventRunEnd)
	foundBeta := false
	for _, item := range second.Snapshot().Items {
		foundBeta = foundBeta || strings.Contains(item.Text, "reply to beta")
	}
	if !foundBeta {
		t.Fatal("second background reply did not reach its original transcript")
	}
	_, entries, err := first.store.LoadEntries(first.SessionID())
	if err != nil || len(entries) < 2 {
		t.Fatalf("first session was not persisted: entries=%d err=%v", len(entries), err)
	}
}

func TestSessionHubKeepsBackgroundApprovalBoundToItsSession(t *testing.T) {
	hub, first, _, project := setupConcurrentHub(t)
	second, err := hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	answer := make(chan judge.ApprovalAnswer, 1)
	go func() {
		answer <- first.confirmApproval(context.Background(), judge.ApprovalRequest{Tool: "bash", Summary: "a command"})
	}()
	event := waitHubEvent(t, hub, first.SessionID(), EventApproval)
	rows, err := hub.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.ID == first.SessionID() {
			found = row.NeedsApproval
		}
	}
	if !found || len(second.Snapshot().Pending) != 0 {
		t.Fatal("background approval was hidden or leaked to another session")
	}
	if _, err := hub.Resume(context.Background(), first.SessionID()); err != nil {
		t.Fatal(err)
	}
	if err := first.RespondApproval(event.ID, AnswerOnce); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-answer:
		if !got.Approve || !got.Answered {
			t.Fatalf("approval = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not reach its original session")
	}
}

func TestSessionHubMarksBackgroundProviderFailure(t *testing.T) {
	hub, first, p, project := setupConcurrentHub(t)
	ctx := context.Background()
	if err := first.Submit(ctx, "fail"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "fail")
	if _, err := hub.NewSession(ctx, project); err != nil {
		t.Fatal(err)
	}
	close(p.release["fail"])
	event := waitHubEvent(t, hub, first.SessionID(), EventRunEnd)
	if event.Error != "provider failed" {
		t.Fatalf("provider error missing from run-end event: %+v", event)
	}
	rows, err := hub.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == first.SessionID() {
			if !row.Unread || !row.Failed || row.Running {
				t.Fatalf("failed background status = %+v", row)
			}
			return
		}
	}
	t.Fatal("failed session missing from sidebar")
}

func TestSessionHubRetainsBackgroundComputerRequest(t *testing.T) {
	hub, first, _, project := setupConcurrentHub(t)
	if _, err := hub.NewSession(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	first.emit(EventComputerRequest, func(event *Event) { event.ID = "Calculator" })
	waitHubEvent(t, hub, first.SessionID(), EventComputerRequest)
	first.emit(EventRunEnd, nil)
	waitHubEvent(t, hub, first.SessionID(), EventRunEnd)
	if got := hub.TakeComputerRequest(first); got != "" {
		t.Fatalf("background request escaped to another session: %q", got)
	}
	rows, err := hub.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var needsAttention bool
	for _, row := range rows {
		if row.ID == first.SessionID() {
			needsAttention = row.NeedsApproval
		}
	}
	if !needsAttention {
		t.Fatal("pending background computer request was not visible")
	}
	if _, err := hub.Resume(context.Background(), first.SessionID()); err != nil {
		t.Fatal(err)
	}
	if got := hub.TakeComputerRequest(first); got != "Calculator" || hub.TakeComputerRequest(first) != "" {
		t.Fatalf("request was lost or replayed: %q", got)
	}
	hub.ParkComputerRequest(first, "Calculator")
	if got := hub.TakeComputerRequest(first); got != "Calculator" {
		t.Fatalf("parked picker request was lost: %q", got)
	}
}

func TestSessionHubCanArchiveAndDeleteUnsavedSessions(t *testing.T) {
	hub, first, _, project := setupConcurrentHub(t)
	second, err := hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.ArchiveSession(context.Background(), first.SessionID(), true); err != nil {
		t.Fatalf("archive unsaved session: %v", err)
	}
	header, _, err := second.store.LoadEntries(first.SessionID())
	if err != nil || header.ArchivedAt == nil {
		t.Fatalf("unsaved session not archived: %+v, %v", header, err)
	}
	third, err := hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.DeleteSession(context.Background(), second.SessionID()); err != nil {
		t.Fatalf("delete unsaved session: %v", err)
	}
	rows, err := hub.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == second.SessionID() {
			t.Fatal("deleted session remains in the hub")
		}
	}
	if hub.Current() != third {
		t.Fatal("deleting a background session changed the current session")
	}
}

func TestSessionHubOpensStoredOtherProjectWithoutInterruptingRun(t *testing.T) {
	hub, first, p, project := setupConcurrentHub(t)
	ctx := context.Background()
	if err := first.Submit(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	waitSessionProvider(t, p, "alpha")
	other := t.TempDir()
	now := time.Now().UTC()
	header := session.SessionHeader{ID: session.NewID(now), CreatedAt: now, UpdatedAt: now, Cwd: other}
	message := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("saved conversation")}}
	if err := first.store.Save(header, agentcore.MessageList{message}); err != nil {
		t.Fatal(err)
	}
	peer, err := hub.Resume(ctx, header.ID)
	if err != nil || peer == first || !first.IsRunning() || !sameProjectPath(peer.Project(), other) {
		t.Fatalf("other project session = %v, %v", peer, err)
	}
	if items := peer.Snapshot().Items; len(items) != 1 || items[0].Text != "saved conversation" {
		t.Fatalf("saved transcript was not restored: %+v", items)
	}
	bad := session.SessionHeader{ID: session.NewID(now.Add(time.Second)), CreatedAt: now, UpdatedAt: now, Cwd: filepath.Join(project, "missing")}
	if err := first.store.Save(bad, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Resume(ctx, bad.ID); err == nil || hub.Current() != peer || !first.IsRunning() {
		t.Fatal("failed switch changed the selected session or interrupted the background run")
	}
	close(p.release["alpha"])
	waitHubEvent(t, hub, first.SessionID(), EventRunEnd)
}
