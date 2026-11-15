//go:build android

package mobilebridge

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/androidagent"
	"github.com/getan/golder/internal/mobileagent"
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
	credentials := provider.NewCredentialStore(nil)
	credentials.SetOverride(name, cfg.APIKey)

	gate := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{}
	}
	toolset, err := androidagent.New(androidagent.Config{
		Root:           cfg.Workspace,
		BeforeToolCall: gate,
		NoShell:        true,
	})
	if err != nil {
		return nil, err
	}
	prompt := cfg.SystemPrompt
	if prompt == "" {
		prompt = toolset.Prompt()
	}
	var debug *os.File
	var observe func(agentcore.AgentEvent)
	if cfg.DebugLog != "" {
		if f, err := os.OpenFile(cfg.DebugLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			debug = f
			observe = debugObserver(f)
		}
	}
	streamFn := provider.StreamFnFromProvider(prov)
	if debug != nil {
		streamFn = debugStream(streamFn, debug)
	}
	agent, err := mobileagent.New(mobileagent.Config{
		Workspace:     cfg.Workspace,
		Model:         cfg.Model,
		Provider:      name,
		Stream:        streamFn,
		GetAPIKey:     credentials.GetAPIKey,
		SystemPrompt:  prompt,
		Batch:         toolset.Batch,
		Permissions:   toolset.Permissions,
		ContextWindow: cfg.ContextWindow,
		ObserveEvent:  observe,
		SessionID:     session.NewID(time.Now()),
	})
	if err != nil {
		if debug != nil {
			_ = debug.Close()
		}
		toolset.Close()
		return nil, fmt.Errorf("mobilebridge: create session: %w", err)
	}
	return &androidBackend{agent: agent, tools: toolset, debug: debug}, nil
}

type androidBackend struct {
	agent *mobileagent.Session
	tools *androidagent.Toolset
	debug *os.File
}

func (b *androidBackend) Start(prompt string) error        { return b.agent.Start(prompt) }
func (b *androidBackend) PollJSON(max int) (string, error) { return b.agent.PollJSON(max) }
func (b *androidBackend) SnapshotJSON() (string, error)    { return b.agent.SnapshotJSON() }
func (b *androidBackend) Cancel()                          { b.agent.Cancel() }
func (b *androidBackend) Close() error {
	err := b.agent.Close()
	b.tools.Close()
	if b.debug != nil {
		_ = b.debug.Close()
	}
	return err
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
