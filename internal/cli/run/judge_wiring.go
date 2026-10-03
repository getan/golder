package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/judge"
	"github.com/smallnest/pigo/internal/permissions"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/seatbelt"
)

// WireBashSandbox injects the sandbox runner into every BashTool in tools. It
// is called from BuiltinTools so every driver (REPL, TUI, headless, /btw side
// runs, task children, sub-agent RPC, webhook) inherits platform isolation
// without its own wiring. The reviewer is injected separately (per turn, by
// the driver) because it depends on the live model, which /model can switch.
//
// The matrix is deliberately conservative:
//
//   - PIGO_SANDBOX=off → no runner: sandbox-tier commands never execute
//     (the gate fails closed) unless a human approved one explicitly.
//   - PIGO_SANDBOX=auto (default) → sandbox-tier verdicts run under
//     sandbox-exec when the platform provides it (macOS).
//   - PIGO_SANDBOX=enforce → every foreground command runs sandboxed and
//     fails closed when no runner is available.
func WireBashSandbox(tools []agentcore.AgentTool, cwd string) {
	mode := seatbelt.ModeFromEnv()
	if mode == seatbelt.ModeOff {
		return
	}
	for _, t := range tools {
		b, ok := t.(*agenttool.BashTool)
		if !ok {
			continue
		}
		switch mode {
		case seatbelt.ModeEnforce:
			b.ForceSandbox = true
			b.Sandbox = seatbelt.New(cwd)
		case seatbelt.ModeAuto:
			b.Sandbox = seatbelt.New(cwd)
		}
	}
}

// WirePermissionState attaches the live approval mode to every BashTool so
// the execution layer honors read-only/full-access and contains an
// auto-mode reviewer failure (see BashTool.sandboxRoute). Task-child tool
// sets are wired with the same state, so a sub-agent cannot outrun the
// parent's mode.
func WirePermissionState(tools []agentcore.AgentTool, state *permissions.State) {
	if state == nil {
		return
	}
	for _, t := range tools {
		if b, ok := t.(*agenttool.BashTool); ok {
			b.Permissions = state
		}
	}
}

// NewReviewer builds the model-backed classifier for one run: the static
// floor chained to the LLM reviewer, which grades calls with the session's
// active model. It is rebuilt per turn so a /model switch changes the
// reviewer with the conversation. A nil provider degrades to the static
// floor plus a fail-closed reviewer (never an allow).
func NewReviewer(model, providerName string, prov provider.Provider, creds *provider.CredentialStore, sessionID, cwd string, trusted bool) judge.Classifier {
	var check judge.ReviewFunc
	if prov != nil {
		check = ReviewCheck(model, providerName, provider.StreamFnFromProvider(prov), creds, sessionID)
	}
	return judge.Chain{Inner: judge.NewLLMJudge(check, cwd, trusted)}
}

// ReviewCheck adapts the provider stream to judge.ReviewFunc: one system +
// user message, drained to a final text answer. Runtime failures (including
// a terminal error/abort) are returned as errors so the reviewer fails
// closed rather than reading an empty answer as approval.
func ReviewCheck(model, providerName string, stream provider.StreamFn, creds *provider.CredentialStore, sessionID string) judge.ReviewFunc {
	if strings.TrimSpace(sessionID) == "" {
		sessionID = provider.ProcessSessionID()
	}
	return func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		if stream == nil {
			return "", errNoReviewer
		}
		apiKey := ""
		if creds != nil {
			apiKey = creds.GetAPIKey(ctx, providerName)
		}
		llm := provider.LlmContext{
			SystemPrompt: systemPrompt,
			Messages: agentcore.MessageList{
				agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content:   agentcore.ContentList{agentcore.NewTextContent(userPrompt)},
				},
			},
		}
		// Carry the session id so providers with sticky routing (opencode's
		// x-opencode-session) route the review next to the conversation that
		// triggered it, exactly as the main loop does.
		s, err := stream(ctx, model, llm, provider.StreamConfig{
			APIKey: apiKey,
			Extra:  provider.WithSessionExtra(nil, sessionID),
		})
		if err != nil {
			return "", err
		}
		for range s.Events() {
		}
		final, resErr := s.Result(ctx)
		if resErr != nil {
			return "", resErr
		}
		switch final.StopReason {
		case agentcore.StopReasonAborted:
			return "", errReviewAborted
		case agentcore.StopReasonError:
			if final.ErrorMessage != "" {
				return "", fmt.Errorf("review failed: %s", final.ErrorMessage)
			}
			return "", fmt.Errorf("review failed: model reported an error")
		}
		return agentcore.ContentToText(final.Content), nil
	}
}

var (
	errNoReviewer    = errors.New("no reviewer model configured")
	errReviewAborted = errors.New("review aborted")
)

// ReviewNotes is the concurrency-safe announcement sink for permission
// decisions. Gates publish notes from the run goroutine (including task-child
// runs); the active driver registers a single handler that forwards them to
// its transcript. A nil handler drops notes.
type ReviewNotes struct {
	mu sync.RWMutex
	fn func(judge.Note)
}

// NewReviewNotes returns an empty sink.
func NewReviewNotes() *ReviewNotes { return &ReviewNotes{} }

// Set replaces the handler. Passing nil drops subsequent notes.
func (r *ReviewNotes) Set(fn func(judge.Note)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fn = fn
}

// Emit delivers one note to the registered handler.
func (r *ReviewNotes) Emit(n judge.Note) {
	if r == nil {
		return
	}
	r.mu.RLock()
	fn := r.fn
	r.mu.RUnlock()
	if fn != nil {
		fn(n)
	}
}

// SandboxGate is the predicate the judge gate asks before failing a
// Sandbox-tier verdict closed: which tool calls this process can actually run
// isolated. It mirrors WireBashSandbox's decision exactly — bash runs under
// sandbox-exec when the mode allows it, the platform provides the binary
// (macOS today), and (in auto mode) a grader is configured — so the gate never
// passes a call through to an execution layer that would run it unsandboxed.
// Nil means nothing can be sandboxed and the gate keeps its prompt/fail-closed
// behavior.
func SandboxGate() func(toolName string) bool {
	mode := seatbelt.ModeFromEnv()
	if mode == seatbelt.ModeOff || !seatbelt.Available() {
		return nil
	}
	return func(toolName string) bool { return strings.EqualFold(strings.TrimSpace(toolName), "bash") }
}
