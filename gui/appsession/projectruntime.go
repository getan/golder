package appsession

import (
	"context"
	"errors"
	"fmt"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
	"github.com/getan/golder/internal/trust"
)

type projectRuntime struct {
	env   run.Env
	live  *cli.LiveConfig
	slash *runtime.SlashRegistry
}

func (c *Controller) prepareProjectRuntime(ctx context.Context, cwd string) (projectRuntime, error) {
	c.mu.Lock()
	factory := c.factory
	current := *c.live
	creds := c.creds
	mode := permissions.Auto
	if c.perms != nil {
		mode = c.perms.Mode()
	}
	manager := c.trust
	configPrompts := append([]string(nil), c.opt.ConfigPrompts...)
	cliPrompts := append([]string(nil), c.opt.CliPrompts...)
	disablePrompts := c.opt.NoPromptTemplates
	c.mu.Unlock()
	if factory == nil {
		return projectRuntime{}, errors.New("this GUI build cannot open another project in the current window")
	}
	if err := ctx.Err(); err != nil {
		return projectRuntime{}, err
	}
	key := ""
	if creds != nil {
		key = creds.GetAPIKey(ctx, current.ProviderName)
	}
	env, err := factory(ctx, cwd, ProjectSetup{
		Model: current.Model, ProviderName: current.ProviderName,
		BaseURL: current.BaseURL, Protocol: current.Protocol, APIKey: key,
	})
	if err != nil {
		return projectRuntime{}, err
	}
	cleanup := func() {
		run.KillShellSessions(env.Tools)
		if env.Extensions != nil {
			_ = env.Extensions.Close()
		}
		if env.Memory != nil {
			_ = env.Memory.Close()
		}
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return projectRuntime{}, err
	}
	if env.Permissions == nil {
		env.Permissions = permissions.New(mode)
	} else {
		env.Permissions.Set(mode)
	}
	if env.ReviewNotes == nil {
		env.ReviewNotes = run.NewReviewNotes()
	}
	if env.Budget == nil {
		env.Budget = contextbudget.New()
	}
	live := &cli.LiveConfig{
		Model: current.Model, ProviderName: env.ProviderName, Provider: env.Provider,
		BaseURL: current.BaseURL, Protocol: current.Protocol,
		ThinkingLevel: current.ThinkingLevel, ContextWindow: cli.ResolveContextWindow(env.ProviderName, current.Model),
		MaxContext: current.MaxContext,
	}
	slash, err := prompts.BuildSlashRegistry(live, env.Credentials, env.Skills, prompts.PromptTemplateSources{
		Extensions: env.Extensions, Settings: configPrompts, CLI: cliPrompts,
		Disable: disablePrompts, ProjectDir: projectPromptDir(env.Cwd),
		ProjectTrusted: manager != nil && manager.IsTrusted(env.Cwd),
	})
	if err != nil {
		cleanup()
		return projectRuntime{}, fmt.Errorf("slash-commands: %w", err)
	}
	trust.RegisterCommand(slash, manager, env.Cwd)
	prompts.RegisterPermissionCommand(slash, env.Permissions, c.conversationLanguage)
	prompts.RegisterBoundaryCommand(slash, c.conversationLanguage)
	if !c.opt.GUI {
		prompts.RegisterPaneCommand(slash, env.Tools)
	}
	return projectRuntime{env: env, live: live, slash: slash}, nil
}

func (c *Controller) conversationLanguage() string {
	c.mu.Lock()
	var messages []agentcore.Message
	if c.agentCtx != nil {
		messages = append(messages, c.agentCtx.Messages...)
	}
	c.mu.Unlock()
	return judge.ConversationLanguage(messages)
}

func (c *Controller) installProjectSession(ctx context.Context, next projectRuntime, header session.SessionHeader, entries []session.Entry, lock *sessionLock, readOnly bool, holder session.WriterInfo, resumed bool) {
	messages := make(agentcore.MessageList, len(entries))
	curLeaf := ""
	for i, entry := range entries {
		messages[i] = entry.Message
		curLeaf = entry.ID
	}
	systemPrompt := next.env.SysPrompt
	if resumed {
		systemPrompt = cli.ResumeSystemPrompt(header, next.env.Prompt)
	}
	agentCtx := &agentcore.AgentContext{SystemPrompt: systemPrompt, Messages: messages, Tools: next.env.Tools}

	c.mu.Lock()
	oldTools := c.opt.Tools
	oldExtensions := c.opt.Extensions
	oldMemory := c.memory
	oldLock := c.lock

	c.opt.Model, c.opt.ProviderName, c.opt.Provider = next.live.Model, next.live.ProviderName, next.live.Provider
	c.opt.BaseURL, c.opt.Protocol = next.live.BaseURL, next.live.Protocol
	c.opt.Tools, c.opt.SysPrompt, c.opt.Prompt = next.env.Tools, next.env.SysPrompt, next.env.Prompt
	c.opt.Skills, c.opt.Extensions = next.env.Skills, next.env.Extensions
	c.opt.Permissions, c.opt.ReviewNotes, c.opt.Budget = next.env.Permissions, next.env.ReviewNotes, next.env.Budget
	c.opt.Credentials, c.opt.Memory, c.opt.Cwd = next.env.Credentials, next.env.Memory, next.env.Cwd

	c.header, c.agentCtx, c.live = header, agentCtx, next.live
	c.reg = run.ToolRegistry(next.env.Tools)
	c.remind = run.TodoReminders(next.env.Tools)
	c.schedule = agenttool.ScheduleFromTools(next.env.Tools)
	c.snap = run.SnapshotRecorderFromTools(next.env.Tools)
	c.creds, c.slash = next.env.Credentials, next.slash
	c.perms, c.notes, c.budget = next.env.Permissions, next.env.ReviewNotes, next.env.Budget
	c.memory, c.cwd, c.prompt = next.env.Memory, next.env.Cwd, next.env.Prompt
	c.lock, c.curLeaf, c.persisted = lock, curLeaf, len(messages)
	c.readOnly, c.writerInfo = readOnly, holder
	c.compacted = false
	c.items, c.index = nil, map[string]int{}
	c.approvedForSession, c.deniedForSession, c.answered = map[string]bool{}, map[string]bool{}, map[string]bool{}
	c.pending = nil
	c.title = "New session"
	if resumed && header.DisplayName != "" {
		c.title = header.DisplayName
	}
	c.dispatcher = nil
	c.hookDeps = run.HookDeps{SessionID: header.ID, ProjectDir: next.env.Cwd, WarnLog: c.warnWriter()}
	c.status = Status{
		Model: next.live.Model, Provider: next.live.ProviderName, Thinking: next.live.ThinkingLevel,
		Permission: next.env.Permissions.Mode().String(), SessionID: header.ID,
		Title: c.title, ContextUsed: estimateTokens(messages), ContextLimit: next.live.ContextWindow,
		ReadOnly: readOnly, Writer: writerView(holder),
	}
	c.mu.Unlock()

	c.installComputerRequest()
	oldLock.release()
	agenttool.ClosePaneAgent(oldTools)
	run.KillShellSessions(oldTools)
	if oldExtensions != nil {
		_ = oldExtensions.Close()
	}
	if oldMemory != nil {
		_ = oldMemory.Close()
	}

	c.seed(messages)
	c.mu.Lock()
	if resumed && header.DisplayName != "" {
		c.title = header.DisplayName
	}
	c.status.Title = c.title
	c.mu.Unlock()
	if !readOnly {
		if err := agenttool.ConfigurePaneAgent(ctx, next.env.Tools, header.ID); err != nil {
			c.warnf("pane control registration unavailable: %v", err)
		}
		c.wireHooks(ctx, resumed)
	}
	c.emitStatus()
}

func closeProjectRuntime(next projectRuntime) {
	run.KillShellSessions(next.env.Tools)
	if next.env.Extensions != nil {
		_ = next.env.Extensions.Close()
	}
	if next.env.Memory != nil {
		_ = next.env.Memory.Close()
	}
}
