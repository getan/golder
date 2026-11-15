package mobileagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// Event types carried across the host boundary.
const (
	EventRunStart   = "run_start"
	EventText       = "text"
	EventThinking   = "thinking"
	EventToolStart  = "tool_start"
	EventToolUpdate = "tool_update"
	EventToolEnd    = "tool_end"
	EventRunEnd     = "run_end"
)

// Event is one ordered notification. Seq is assigned by the session and
// increases monotonically for its lifetime, so a host can detect a missed
// event. Type selects which of the remaining fields are meaningful. All fields
// are JSON-safe so a JNI/Objective-C bridge can forward the marshaled value
// without inspecting Go types.
type Event struct {
	Seq        uint64          `json:"seq"`
	RunID      string          `json:"runId,omitempty"`
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	StopReason string          `json:"stopReason,omitempty"`
	Error      string          `json:"error,omitempty"`
	Cancelled  bool            `json:"cancelled,omitempty"`
}

// Config is supplied by the mobile host, never by the model. Workspace, Stream
// and Model are required. Tools and Batch must describe the same catalog; when
// Registry is set, Tools is replaced with the registry's list so the model
// cannot be offered a tool the executor does not know. A non-empty tool catalog
// requires an authorization gate (Batch.BeforeToolCall): the session never
// manufactures an unconfined-execution grant.
type Config struct {
	// Workspace is the host-selected project directory. Tools receive their own
	// roots from the host; this path is validated and recorded for the host.
	Workspace string
	Model     string
	Provider  string

	// Stream produces the provider stream. A mobile host normally wires the
	// real provider; tests use a scripted one.
	Stream provider.StreamFn
	// APIKey is the static fallback. GetAPIKey, when set, is consulted per
	// request and wins over APIKey, which is how a host refreshes short-lived
	// credentials without rebuilding the session.
	APIKey    string
	GetAPIKey func(context.Context, string) string

	SystemPrompt string
	// History restores an earlier conversation. The slice is copied.
	History agentcore.MessageList

	// Tools is the model-visible catalog. When Batch.Registry is set, the
	// registry's tools replace this field.
	Tools []agentcore.AgentTool
	Batch agenttool.BatchConfig
	// Permissions is the live approval mode shared with the tool set. Nil
	// selects auto mode for hosts that inspect it; the gate is still
	// Batch.BeforeToolCall.
	Permissions *permissions.State

	ThinkingLevel agentcore.ThinkingLevel
	ContextWindow int
	Compaction    compaction.CompactionSettings
	// Checkpoint is the loop's mid-run persistence seam. It runs on the loop
	// goroutine and must not block on user input.
	Checkpoint  func(context.Context, *agentcore.AgentContext, bool)
	SessionID   string
	EventBuffer int
}

// Snapshot is the state a host reads after a missed event or a view rebuild.
// Messages is a shallow copy of the conversation; message values are immutable
// once appended by the loop.
type Snapshot struct {
	Running  bool                  `json:"running"`
	RunID    string                `json:"runId,omitempty"`
	Seq      uint64                `json:"seq"`
	Pending  int                   `json:"pending"`
	Messages agentcore.MessageList `json:"messages"`
}

// Session owns one mobile conversation. It is safe for concurrent use; at most
// one run is active at a time. Poll (or PollJSON) is the intended bridge: a
// host drains events on its own thread rather than blocking a UI thread inside
// a JNI callback.
type Session struct {
	cfg   runtime.RunConfig
	ctx   *agentcore.AgentContext
	root  string
	perms *permissions.State

	mu       sync.Mutex
	events   []Event
	messages agentcore.MessageList
	nextSeq  uint64
	running  bool
	cancel   context.CancelFunc
	runID    string
	nextRun  int
	closed   bool
	wg       sync.WaitGroup
}

// New validates cfg and builds a session. It does not open the network or run
// the model.
func New(cfg Config) (*Session, error) {
	root, err := cleanWorkspace(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("mobileagent: model is required")
	}
	if cfg.Stream == nil {
		return nil, errors.New("mobileagent: provider stream is required")
	}
	tools := cfg.Tools
	if cfg.Batch.Registry != nil {
		tools = cfg.Batch.Registry.List()
	}
	if len(tools) > 0 && cfg.Batch.BeforeToolCall == nil {
		return nil, errors.New("mobileagent: host authorization gate is required for tools")
	}
	perms := cfg.Permissions
	if perms == nil {
		perms = permissions.New(permissions.Auto)
	}
	runCfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         cfg.Model,
			Provider:      cfg.Provider,
			Stream:        cfg.Stream,
			APIKey:        cfg.APIKey,
			GetAPIKey:     cfg.GetAPIKey,
			ThinkingLevel: cfg.ThinkingLevel,
			ContextWindow: cfg.ContextWindow,
			Compaction:    cfg.Compaction,
		},
		Batch:       cfg.Batch,
		Checkpoint:  cfg.Checkpoint,
		SessionID:   cfg.SessionID,
		EventBuffer: cfg.EventBuffer,
	}
	history := make(agentcore.MessageList, len(cfg.History))
	copy(history, cfg.History)
	published := make(agentcore.MessageList, len(history))
	copy(published, history)
	return &Session{
		cfg:      runCfg,
		ctx:      &agentcore.AgentContext{SystemPrompt: cfg.SystemPrompt, Messages: history, Tools: tools},
		root:     root,
		perms:    perms,
		messages: published,
	}, nil
}

func cleanWorkspace(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("mobileagent: workspace is required")
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("mobileagent: workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("mobileagent: workspace is not a readable directory: %s", root)
	}
	return root, nil
}

// Workspace returns the validated project directory.
func (s *Session) Workspace() string { return s.root }

// Permissions returns the live approval state the host shares with its tools.
func (s *Session) Permissions() *permissions.State { return s.perms }

// Start begins one run. It returns immediately; the result arrives as events.
// An empty prompt, a closed session, or a run already in flight is an error.
func (s *Session) Start(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("mobileagent: prompt is empty")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("mobileagent: session is closed")
	}
	if s.running {
		s.mu.Unlock()
		return errors.New("mobileagent: a run is already active")
	}
	s.nextRun++
	runID := fmt.Sprintf("run-%d", s.nextRun)
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.cancel = cancel
	s.runID = runID
	s.ctx.Messages = append(s.ctx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(prompt)},
	})
	s.messages = append(cloneMessages(s.messages), s.ctx.Messages[len(s.ctx.Messages)-1])
	s.appendLocked(Event{RunID: runID, Type: EventRunStart})
	s.mu.Unlock()

	s.wg.Add(1)
	go s.run(ctx, runID)
	return nil
}

func (s *Session) run(ctx context.Context, runID string) {
	defer s.wg.Done()
	cfg := s.cfg
	hostCheckpoint := cfg.Checkpoint
	cfg.Checkpoint = func(checkpointCtx context.Context, agentCtx *agentcore.AgentContext, compacted bool) {
		s.publishMessages(agentCtx.Messages)
		if hostCheckpoint != nil {
			hostCheckpoint(checkpointCtx, agentCtx, compacted)
		}
	}
	stream := runtime.StartRun(ctx, s.ctx, cfg)

	thinkingPrinted := 0
	final, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnText: func(delta string) {
			if delta != "" {
				s.append(Event{RunID: runID, Type: EventText, Text: delta})
			}
		},
		OnEvent: func(ev agentcore.AgentEvent) {
			switch e := ev.(type) {
			case agentcore.MessageUpdateEvent:
				msg, ok := e.Message.(agentcore.AssistantMessage)
				if !ok {
					return
				}
				text := thinkingText(msg.Content)
				if len(text) > thinkingPrinted {
					s.append(Event{RunID: runID, Type: EventThinking, Text: text[thinkingPrinted:]})
					thinkingPrinted = len(text)
				}
			case agentcore.ToolExecutionStartEvent:
				s.append(Event{RunID: runID, Type: EventToolStart, ToolCallID: e.ToolCallID, ToolName: e.ToolName, Args: marshalAny(e.Args)})
			case agentcore.ToolExecutionUpdateEvent:
				s.append(Event{RunID: runID, Type: EventToolUpdate, ToolCallID: e.ToolCallID, ToolName: e.ToolName, Text: agentcore.ContentToText(e.PartialResult.Content)})
			case agentcore.ToolExecutionEndEvent:
				s.append(Event{
					RunID: runID, Type: EventToolEnd, ToolCallID: e.ToolCallID, ToolName: e.ToolName,
					Text: agentcore.ContentToText(e.Result.Content), Details: marshalAny(e.Result.Details),
					IsError: e.IsError || e.Result.IsError,
				})
			}
		},
		OnTurnEnd: func(agentcore.AssistantMessage, []agentcore.ToolResultMessage, runtime.TurnStall) {
			thinkingPrinted = 0
		},
	})

	cancelled := ctx.Err() != nil
	errText, stop := runOutcome(final, err, cancelled)
	s.finish(Event{RunID: runID, Type: EventRunEnd, StopReason: stop, Error: errText, Cancelled: cancelled})
}

func runOutcome(final *agentcore.AssistantMessage, err error, cancelled bool) (string, string) {
	stop := ""
	if final != nil {
		stop = final.StopReason
	}
	if cancelled {
		return "", stop
	}
	if err != nil {
		return err.Error(), stop
	}
	if final != nil && final.StopReason == agentcore.StopReasonError {
		if msg := strings.TrimSpace(final.ErrorMessage); msg != "" {
			return msg, stop
		}
		return "the provider returned an error with no message", stop
	}
	return "", stop
}

// finish publishes the terminal event before another run can start, so a host
// never observes a new run_start before the previous run_end.
func (s *Session) finish(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// DrainStream has returned, so the loop goroutine no longer mutates the
	// context and this final copy is safe.
	s.messages = cloneMessages(s.ctx.Messages)
	s.appendLocked(e)
	s.running = false
	s.cancel = nil
}

// publishMessages snapshots the loop-owned conversation at a checkpoint. The
// loop mutates AgentContext.Messages on its own goroutine, so Snapshot must
// never read that slice directly; checkpoints and run end are the consistent
// points where a copy can be taken.
func (s *Session) publishMessages(msgs agentcore.MessageList) {
	copied := cloneMessages(msgs)
	s.mu.Lock()
	s.messages = copied
	s.mu.Unlock()
}

func cloneMessages(msgs agentcore.MessageList) agentcore.MessageList {
	out := make(agentcore.MessageList, len(msgs))
	copy(out, msgs)
	return out
}

func thinkingText(content agentcore.ContentList) string {
	var b strings.Builder
	for _, block := range content {
		if th, ok := block.(agentcore.ThinkingContent); ok {
			b.WriteString(th.Thinking)
		}
	}
	return b.String()
}

func marshalAny(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// Poll removes and returns up to max queued events. max <= 0 returns all.
func (s *Session) Poll(max int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.events)
	if max > 0 && max < n {
		n = max
	}
	out := make([]Event, n)
	copy(out, s.events[:n])
	s.events = append([]Event(nil), s.events[n:]...)
	for i := range out {
		out[i].Args = cloneRaw(out[i].Args)
		out[i].Details = cloneRaw(out[i].Details)
	}
	return out
}

// PollJSON is Poll for a bridge that forwards JSON instead of translating Go
// values. An empty queue is "[]", never "".
func (s *Session) PollJSON(max int) (string, error) {
	events := s.Poll(max)
	if events == nil {
		events = []Event{}
	}
	raw, err := json.Marshal(events)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// append adds one event, coalescing adjacent deltas so a chatty provider does
// not produce one JNI call per token.
func (s *Session) append(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLocked(e)
}

func (s *Session) appendLocked(e Event) {
	if n := len(s.events); n > 0 {
		last := &s.events[n-1]
		if last.RunID == e.RunID {
			switch {
			case last.Type == EventText && e.Type == EventText:
				last.Text += e.Text
				return
			case last.Type == EventThinking && e.Type == EventThinking:
				last.Text += e.Text
				return
			case last.Type == EventToolUpdate && e.Type == EventToolUpdate && last.ToolCallID == e.ToolCallID:
				last.Text += e.Text
				return
			}
		}
	}
	e.Seq = s.nextSeq
	s.nextSeq++
	s.events = append(s.events, e)
}

// Snapshot returns a consistent view for a view rebuild.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Running:  s.running,
		RunID:    s.runID,
		Seq:      s.nextSeq,
		Pending:  len(s.events),
		Messages: cloneMessages(s.messages),
	}
}

// SnapshotJSON is Snapshot for a JSON bridge.
func (s *Session) SnapshotJSON() (string, error) {
	raw, err := json.Marshal(s.Snapshot())
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Running reports whether a run is active (including its final event not yet
// polled).
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Cancel asks the active run to stop. It is a no-op when idle. The run_end
// event reports Cancelled once the loop has drained.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close cancels any active run, waits for it to drain, and makes the session
// unusable. It does not close host-owned tools; the host remains responsible
// for the tool set it built.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}
