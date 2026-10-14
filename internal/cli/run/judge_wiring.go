package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/jev"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/reviewpolicy"
	"github.com/getan/golder/internal/seatbelt"
)

// WireBashSandbox injects the sandbox runner into every BashTool in tools. It
// is called from BuiltinTools so every driver (REPL, TUI, headless, /btw side
// runs, task children, sub-agent RPC, webhook) inherits platform isolation
// without its own wiring. The reviewer is injected separately (per turn, by
// the driver) because it depends on the live model, which /model can switch.
//
// The matrix is deliberately conservative:
//
//   - GOLDER_SANDBOX=off → no runner: sandbox-tier commands never execute
//     (the gate fails closed) unless a human approved one explicitly.
//   - GOLDER_SANDBOX=auto (default) → sandbox-tier verdicts run under the
//     platform sandbox when it provides one (sandbox-exec on macOS,
//     bubblewrap on Linux).
//   - GOLDER_SANDBOX=enforce → every foreground command runs sandboxed and
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
// floor, chained to the decision-model router, which falls back to the LLM
// reviewer that grades calls with the session's active model. It is rebuilt per
// turn so a /model switch changes the reviewer with the conversation. A nil
// provider degrades to the static floor plus a fail-closed reviewer (never an
// allow).
func NewReviewer(model, providerName string, prov provider.Provider, creds *provider.CredentialStore, sessionID, cwd string, trusted bool) judge.Classifier {
	var check judge.ReviewFunc
	if prov != nil {
		check = ReviewCheck(model, providerName, provider.StreamFnFromProvider(prov), creds, sessionID)
	}
	llm := judge.NewLLMJudge(check, cwd, trusted)
	return judge.Chain{Inner: JevRouter(llm, creds, cwd), WorkspaceRoot: cwd}
}

// DefaultJevEndpoint is TypeSafe's evaluation endpoint.
const DefaultJevEndpoint = "https://api.typesafe.ai/v1/systemone"

// JevSettings is the decision-model reviewer's resolved configuration.
type JevSettings struct {
	// Enabled reports whether the cheap judge participates. It is ON by
	// default: a run that has a TypeSafe credential gets the fast path, and a
	// run without one behaves exactly as it did before this layer existed.
	Enabled bool
	// Endpoint is where evaluations are sent.
	Endpoint string
	// Timeout bounds one evaluation.
	Timeout time.Duration
}

// JevSettingsFromEnv resolves the settings. GOLDER_JEV=off (or 0/false/no/
// disabled) turns the cheap judge off entirely; GOLDER_JEV_ENDPOINT and
// GOLDER_JEV_TIMEOUT_MS override the endpoint and the per-call timeout.
func JevSettingsFromEnv(getenv func(string) string) JevSettings {
	if getenv == nil {
		getenv = os.Getenv
	}
	out := JevSettings{Enabled: true, Endpoint: DefaultJevEndpoint, Timeout: 10 * time.Second}
	switch strings.ToLower(strings.TrimSpace(getenv("GOLDER_JEV"))) {
	case "off", "0", "false", "no", "disabled":
		out.Enabled = false
	}
	if ep := strings.TrimSpace(getenv("GOLDER_JEV_ENDPOINT")); ep != "" {
		out.Endpoint = ep
	}
	if raw := strings.TrimSpace(getenv("GOLDER_JEV_TIMEOUT_MS")); raw != "" {
		if ms, err := time.ParseDuration(raw + "ms"); err == nil && ms > 0 {
			out.Timeout = ms
		}
	}
	return out
}

// JevRouter wraps a reviewer with the decision-model fast path. It returns the
// reviewer unchanged when the feature is off or no credential exists, so the
// caller gets byte-for-byte the behaviour it had before — an absent key must
// never add a failed round trip to every gated call.
func JevRouter(fallback judge.Classifier, creds *provider.CredentialStore, cwd string) judge.Classifier {
	settings := JevSettingsFromEnv(os.Getenv)
	if !settings.Enabled {
		return fallback
	}
	// The key is resolved through the credential store, so --api-key, the
	// config layer and TYPESAFE_API_KEY all work and a refresh is picked up
	// between attempts. The presence probe keeps a keyless run on the old path.
	hasKey := false
	if creds != nil {
		hasKey = creds.HasCredential(context.Background(), "typesafe")
	}
	if !hasKey {
		return fallback
	}
	return &reviewpolicy.Classifier{
		Client: jev.New(jev.Config{
			Endpoint: settings.Endpoint,
			Timeout:  settings.Timeout,
			APIKeyFunc: func(ctx context.Context) string {
				if creds == nil {
					return ""
				}
				return creds.GetAPIKey(ctx, "typesafe")
			},
			// The user's proxy selection applies: a host that cannot reach
			// TypeSafe directly configures it in /proxy like any provider.
			HTTPClient: provider.HTTPClientFor("typesafe", settings.Endpoint),
		}),
		Fallback: fallback,
		Config:   reviewpolicy.DefaultConfig(),
		Cwd:      cwd,
		// The same extra roots the gate chain enforces with, so the reviewer's
		// view of the boundary matches the gate's rather than being one directory
		// stricter (which reads a skills read as a boundary crossing).
		ExtraRoots: ReadableExtraRoots(),
	}
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
// the platform sandbox (sandbox-exec on macOS, bubblewrap on Linux) when the
// mode allows it and the platform provides it, and (in auto mode) a grader is
// configured — so the gate never passes a call through to an execution layer
// that would run it unsandboxed. Nil means nothing can be sandboxed and the
// gate keeps its prompt/fail-closed
// behavior.
func SandboxGate() func(toolName string) bool {
	mode := seatbelt.ModeFromEnv()
	if mode == seatbelt.ModeOff || !seatbelt.Available() {
		return nil
	}
	return func(toolName string) bool { return strings.EqualFold(strings.TrimSpace(toolName), "bash") }
}
