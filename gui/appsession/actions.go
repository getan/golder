package appsession

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// GUI-only action IDs. They are not slash commands: they exist because a
// window can do something a terminal cannot.
const (
	ActionNewSession = "gui:new-session"
	ActionCompact    = "gui:compact"
	ActionCopyLast   = "gui:copy-last"
)

// ListActions returns every action the command palette can run: the session's
// slash commands plus the window-only actions.
func (c *Controller) ListActions(ctx context.Context) []Action {
	c.mu.Lock()
	slash := c.slash
	c.mu.Unlock()

	var out []Action
	out = append(out,
		Action{ID: ActionNewSession, Label: "New Session", Category: "Session", Description: "Start a fresh conversation in this project."},
		Action{ID: ActionCompact, Label: "Compact Conversation", Category: "Session", Description: "Summarize the history to free context."},
	)
	if slash != nil {
		for _, cmd := range slash.List() {
			if !runtime.TopLevelVisible(cmd) {
				continue
			}
			out = append(out, Action{
				ID:           cmd.Name,
				Label:        "/" + cmd.Name,
				Category:     runtime.CategoryOf(cmd),
				Description:  cmd.Description,
				ArgumentHint: cmd.ArgumentHint,
				Source:       cmd.Source.String(),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// RunAction runs one action by ID. A prompt-producing command is submitted as
// the next turn; an action command's status line is returned as Message.
func (c *Controller) RunAction(ctx context.Context, id, args string) (ActionResult, error) {
	switch id {
	case ActionNewSession:
		return ActionResult{Message: "Started a new session."}, c.NewSession(ctx)
	case ActionCompact:
		msg, err := c.Compact(ctx)
		return ActionResult{Message: msg}, err
	}

	c.mu.Lock()
	slash := c.slash
	c.mu.Unlock()
	if slash == nil {
		return ActionResult{}, errors.New("no command registry")
	}
	line := "/" + strings.TrimPrefix(id, "/")
	if strings.TrimSpace(args) != "" {
		line += " " + args
	}
	outcome, err := slash.ResolveOutcome(line)
	if err != nil {
		return ActionResult{}, err
	}
	res := ActionResult{Message: outcome.Message}
	if outcome.Prompt != "" {
		if err := c.Submit(ctx, outcome.Prompt); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Compact replaces the context with a fresh summary plus the recent tail.
func (c *Controller) Compact(ctx context.Context) (string, error) {
	if c.IsRunning() {
		return "", ErrBusy
	}
	c.mu.Lock()
	if c.readOnly {
		c.mu.Unlock()
		return "", ErrReadOnly
	}
	live := c.live
	creds := c.creds
	agentCtx := c.agentCtx
	msgs := agentCtx.Messages
	settings := live.CompactionSettings()
	window := live.ContextWindow
	model := provider.Model{Provider: live.ProviderName, ID: live.Model, ContextWindow: window}
	stream := provider.StreamFnFromProvider(live.Provider)
	providerName := live.ProviderName
	c.mu.Unlock()

	before := estimateTokens(msgs)
	scfg := provider.StreamConfig{}
	if creds != nil {
		scfg.APIKey = creds.GetAPIKey(ctx, providerName)
	}
	res, err := compaction.Compact(ctx, stream, model, msgs, settings, window, -1, nil, "", scfg)
	if err != nil {
		return "", err
	}
	if res == nil {
		return fmt.Sprintf("Nothing to compact (%d tokens, %d messages).", before, len(msgs)), nil
	}
	rebuilt := res.RebuildContext(msgs, timeNowMillis())
	c.mu.Lock()
	c.agentCtx.Messages = rebuilt
	c.compacted = true
	c.status.ContextUsed = estimateTokens(rebuilt)
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		return "", err
	}
	after := estimateTokens(rebuilt)
	msg := fmt.Sprintf("Compacted: %d → %d tokens, summarized %d messages, kept %d.",
		before, after, len(msgs)-(len(rebuilt)-1), len(rebuilt)-1)
	c.addNotice(msg, false)
	c.emitStatus()
	return msg, nil
}
