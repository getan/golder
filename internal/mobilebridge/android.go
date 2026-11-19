//go:build android

package mobilebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/androidagent"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/mobileagent"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

// NewAndroidSession builds the first Android backend: the real provider, the
// real Android tool loop, and a file-only catalog. The shell is deliberately
// omitted until a command sandbox and a per-call host gate exist. The gate
// below allows the catalog's file tools; it cannot grant a tool that is not in
// the catalog, and the catalog has no shell, browser or network tool.
func NewAndroidSession(cfg Config) (Backend, error) {
	prov, name, err := provider.ResolveProvider(cfg.Model, cfg.BaseURL, cfg.Protocol, cfg.Provider, os.Getenv)
	if err != nil {
		return nil, err
	}
	contextWindow := cfg.ContextWindow
	if contextWindow <= 0 {
		contextWindow = provider.ContextWindowFor(name, cfg.Model)
	}
	if contextWindow <= 0 {
		// Custom OpenAI-compatible models often have no catalog entry. Keep the
		// mid-run context gauge useful; the host can override via ContextWindow.
		contextWindow = 200_000
	}
	permissionMode := permissions.Auto
	if cfg.PermissionMode != "" {
		if parsed, ok := permissions.Parse(cfg.PermissionMode); ok {
			permissionMode = parsed
		}
	}
	permissionState := permissions.New(permissionMode)
	credentials := provider.NewCredentialStore(nil)
	credentials.SetOverride(name, cfg.APIKey)

	gate := func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if call.Name == "android_use" {
			var args struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			switch args.Action {
			case "tap", "type", "swipe", "open_app", "back", "home",
				"background_start", "background_open_app", "background_tap",
				"phase2_start", "phase2_open_app", "phase2_tap":
				if permissionMode == permissions.ReadOnly {
					content := agentcore.ContentList{agentcore.NewTextContent("AndroidUse control actions are disabled in read-only mode")}
					return &agentcore.BeforeToolCallDecision{Block: true, Content: &content}
				}
			}
		}
		if call.Name == "android_build" || call.Name == "android_download" {
			return &agentcore.BeforeToolCallDecision{Network: true}
		}
		if call.Name == "git" && gitNeedsNetwork(call.Arguments) {
			if !cfg.AllowNetworkGit {
				content := agentcore.ContentList{agentcore.NewTextContent("network Git is disabled by the host; local git operations are available")}
				return &agentcore.BeforeToolCallDecision{Block: true, Content: &content}
			}
			return &agentcore.BeforeToolCallDecision{Network: true}
		}
		return &agentcore.BeforeToolCallDecision{}
	}
	toolset, err := androidagent.New(androidagent.Config{
		Root:               cfg.Workspace,
		BeforeToolCall:     gate,
		Permissions:        permissionState,
		NoShell:            true,
		GitAuthorName:      cfg.GitAuthorName,
		GitAuthorEmail:     cfg.GitAuthorEmail,
		GitToken:           cfg.GitToken,
		AndroidUseEndpoint: cfg.AndroidUseEndpoint,
		AndroidUsePhase2:   cfg.AndroidUsePhase2,
		SearchEnv: func(name string) string {
			switch name {
			case "TAVILY_API_KEY":
				return cfg.TavilyAPIKey
			case "EXA_API_KEY":
				return cfg.ExaAPIKey
			case "DEEPSEEK_API_KEY":
				return cfg.DeepSeekAPIKey
			default:
				return ""
			}
		},
	})
	if err != nil {
		return nil, err
	}
	var debug *os.File
	var observe func(agentcore.AgentEvent)
	if cfg.DebugLog != "" {
		if f, err := os.OpenFile(cfg.DebugLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			debug = f
			observe = debugObserver(f)
		}
	}
	var store *session.Store
	var header session.SessionHeader
	var lock *session.WriterLock
	var history agentcore.MessageList
	if strings.TrimSpace(cfg.StateDir) != "" {
		var openErr error
		store, header, history, lock, openErr = openMobileStore(cfg)
		if openErr != nil {
			if debug != nil {
				_ = debug.Close()
			}
			toolset.Close()
			return nil, openErr
		}
	}
	// The restored conversation is retained, but a host-generated prompt must
	// reflect today's tool catalog and permissions, not a previous app version.
	prompt := selectMobilePrompt(cfg.SystemPrompt, header.SystemPrompt, toolset.Prompt())
	sessionID := ""
	if store != nil {
		sessionID = header.ID
		header.Model, header.Provider = cfg.Model, name
		header.SystemPrompt, header.Cwd = prompt, cfg.Workspace
		header.UpdatedAt = time.Now().UTC()
		if header.CreatedAt.IsZero() {
			header.CreatedAt = time.Now().UTC()
		}
	} else {
		sessionID = session.NewID(time.Now())
	}
	streamFn := provider.StreamFnFromProvider(prov)
	if debug != nil {
		streamFn = debugStream(streamFn, debug)
	}
	backend := &androidBackend{
		tools: toolset, debug: debug, store: store, header: header, lock: lock,
	}
	agent, err := mobileagent.New(mobileagent.Config{
		Workspace:     cfg.Workspace,
		Model:         cfg.Model,
		Provider:      name,
		Stream:        streamFn,
		GetAPIKey:     credentials.GetAPIKey,
		SystemPrompt:  prompt,
		History:       history,
		Batch:         toolset.Batch,
		ContextWindow: contextWindow,
		Compaction:    compaction.DefaultCompactionSettings,
		ObserveEvent:  observe,
		ThinkingLevel: agentcore.ThinkingLevel(cfg.ThinkingLevel),
		Permissions:   permissionState,
		Checkpoint:    func(_ context.Context, agentCtx *agentcore.AgentContext, _ bool) { backend.save(agentCtx.Messages) },
		OnRunEnd:      backend.save,
		SessionID:     sessionID,
	})
	if err != nil {
		if debug != nil {
			_ = debug.Close()
		}
		if lock != nil {
			lock.Release()
		}
		toolset.Close()
		return nil, fmt.Errorf("mobilebridge: create session: %w", err)
	}
	backend.agent = agent
	return backend, nil
}

type androidBackend struct {
	agent     *mobileagent.Session
	tools     *androidagent.Toolset
	debug     *os.File
	store     *session.Store
	header    session.SessionHeader
	lock      *session.WriterLock
	persistMu sync.Mutex
}

func (b *androidBackend) Start(prompt string) error        { return b.agent.Start(prompt) }
func (b *androidBackend) PollJSON(max int) (string, error) { return b.agent.PollJSON(max) }
func (b *androidBackend) SnapshotJSON() (string, error)    { return b.agent.SnapshotJSON() }
func (b *androidBackend) Cancel()                          { b.agent.Cancel() }
func (b *androidBackend) PauseForTakeover() error          { return b.agent.PauseForTakeover() }
func (b *androidBackend) TakeoverReady() bool              { return b.agent.TakeoverReady() }
func (b *androidBackend) ResumeAfterTakeover()             { b.agent.ResumeAfterTakeover() }
func (b *androidBackend) Close() error {
	var err error
	if b.agent != nil {
		err = b.agent.Close()
		b.save(b.agent.Snapshot().Messages)
	}
	b.tools.Close()
	if b.lock != nil {
		b.lock.Release()
	}
	if b.debug != nil {
		_ = b.debug.Close()
	}
	return err
}

// save rewrites the session atomically. The initial implementation trades
// write amplification for a simple, crash-safe persistence boundary; callers
// see the same JSONL schema as the CLI.
func (b *androidBackend) save(msgs agentcore.MessageList) {
	if b.store == nil {
		return
	}
	b.persistMu.Lock()
	defer b.persistMu.Unlock()
	b.header.UpdatedAt = time.Now().UTC()
	if err := b.store.Save(b.header, withoutAndroidUseImages(msgs)); err != nil && b.debug != nil {
		fmt.Fprintf(b.debug, "persist_error=%v\n", err)
	}
}

func gitNeedsNetwork(raw json.RawMessage) bool {
	var a struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "clone", "fetch", "push":
		return true
	default:
		return false
	}
}

func openMobileStore(cfg Config) (*session.Store, session.SessionHeader, agentcore.MessageList, *session.WriterLock, error) {
	store, err := session.NewStore(filepath.Join(cfg.StateDir, "sessions"))
	if err != nil {
		return nil, session.SessionHeader{}, nil, nil, fmt.Errorf("mobilebridge: session store: %w", err)
	}
	id := strings.TrimSpace(cfg.SessionID)
	if id == "" {
		id = session.NewID(time.Now())
	}
	if err := session.ValidateID(id); err != nil {
		return nil, session.SessionHeader{}, nil, nil, err
	}
	lock, err := store.AcquireWriter(id, "Golder Android")
	if err != nil {
		return nil, session.SessionHeader{}, nil, nil, err
	}
	header := session.SessionHeader{
		ID: id, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Model: cfg.Model, Cwd: cfg.Workspace,
	}
	var history agentcore.MessageList
	path := filepath.Join(store.Dir(), session.FileName(id))
	if _, statErr := os.Stat(path); statErr == nil {
		loaded, messages, loadErr := store.Load(id)
		if loadErr != nil {
			lock.Release()
			return nil, session.SessionHeader{}, nil, nil, fmt.Errorf("mobilebridge: load session: %w", loadErr)
		}
		header, history = loaded, messages
		// An older build could have saved megabytes of screenshots, which stalls
		// the next request. Drop them and rewrite the file once.
		if hasAndroidUseImages(history) {
			history = withoutAndroidUseImages(history)
			if err := store.Save(header, history); err != nil {
				lock.Release()
				return nil, session.SessionHeader{}, nil, nil, fmt.Errorf("mobilebridge: trim screenshots: %w", err)
			}
		}
	} else if !os.IsNotExist(statErr) {
		lock.Release()
		return nil, session.SessionHeader{}, nil, nil, fmt.Errorf("mobilebridge: session file: %w", statErr)
	}
	return store, header, history, lock, nil
}

// debugObserver writes only event types and terminal diagnostics. It must not
// copy prompts, tool arguments or results: this file is for host debugging and
// may outlive the process.
func debugObserver(f *os.File) func(agentcore.AgentEvent) {
	return func(ev agentcore.AgentEvent) {
		switch e := ev.(type) {
		case agentcore.MessageEndEvent:
			if msg, ok := e.Message.(agentcore.AssistantMessage); ok {
				fmt.Fprintf(f, "message_end stop=%s error=%q text_bytes=%d\n", msg.StopReason, msg.ErrorMessage, len(agentcore.ContentToText(msg.Content)))
				return
			}
			fmt.Fprintf(f, "message_end %T\n", e.Message)
		case agentcore.TurnEndEvent:
			fmt.Fprintf(f, "turn_end stop=%s error=%q text_bytes=%d tools=%d\n", e.Message.StopReason, e.Message.ErrorMessage, len(agentcore.ContentToText(e.Message.Content)), len(e.ToolResults))
		case agentcore.ProviderNoticeEvent:
			fmt.Fprintf(f, "provider_notice kind=%s reason=%s attempt=%d window=%s\n", e.Kind, e.Reason, e.Attempt, e.Window)
		case agentcore.CompactionEvent:
			fmt.Fprintf(f, "compaction error=%q\n", e.ErrorMessage)
		default:
			fmt.Fprintf(f, "%s\n", ev.EventType())
		}
	}
}

// debugStream tees provider events into the debug log without copying their
// content. It exists only when DebugLog is set, so a host can tell a stalled
// HTTP read from a stalled agent loop.
func debugStream(inner provider.StreamFn, f *os.File) provider.StreamFn {
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		fmt.Fprintf(f, "stream_call model=%s messages=%d\n", model, len(llm.Messages))
		src, err := inner(ctx, model, llm, cfg)
		if err != nil {
			fmt.Fprintf(f, "stream_build_error=%v\n", err)
			return nil, err
		}
		dst := provider.NewAssistantMessageEventStream(0)
		go func() {
			defer dst.Close()
			for ev := range src.Events() {
				fmt.Fprintf(f, "stream_event %T\n", ev)
				if err := dst.Emit(ctx, ev); err != nil {
					return
				}
			}
			final, err := src.Result(ctx)
			if err != nil {
				dst.SetError(err)
				return
			}
			dst.SetResult(final)
		}()
		return dst, nil
	}
}
