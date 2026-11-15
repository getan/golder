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
	agent, err := mobileagent.New(mobileagent.Config{
		Workspace:     cfg.Workspace,
		Model:         cfg.Model,
		Provider:      name,
		Stream:        provider.StreamFnFromProvider(prov),
		GetAPIKey:     credentials.GetAPIKey,
		SystemPrompt:  prompt,
		Batch:         toolset.Batch,
		Permissions:   toolset.Permissions,
		ContextWindow: cfg.ContextWindow,
		SessionID:     session.NewID(time.Now()),
	})
	if err != nil {
		toolset.Close()
		return nil, fmt.Errorf("mobilebridge: create session: %w", err)
	}
	return &androidBackend{agent: agent, tools: toolset}, nil
}

type androidBackend struct {
	agent *mobileagent.Session
	tools *androidagent.Toolset
}

func (b *androidBackend) Start(prompt string) error        { return b.agent.Start(prompt) }
func (b *androidBackend) PollJSON(max int) (string, error) { return b.agent.PollJSON(max) }
func (b *androidBackend) SnapshotJSON() (string, error)    { return b.agent.SnapshotJSON() }
func (b *androidBackend) Cancel()                          { b.agent.Cancel() }
func (b *androidBackend) Close() error {
	err := b.agent.Close()
	b.tools.Close()
	return err
}
