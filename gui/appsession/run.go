package appsession

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/compactionlog"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// Submit sends one prompt as a new turn.
func (c *Controller) Submit(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.agentCtx == nil {
		c.mu.Unlock()
		return errors.New("controller is not open")
	}
	if c.running {
		c.mu.Unlock()
		return ErrBusy
	}
	agentCtx := c.agentCtx
	dispatcher := c.dispatcher
	deps := c.hookDeps
	reminders := c.remind
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	c.running, c.runID = true, runID
	c.status.Running, c.status.RunID, c.status.Error = true, runID, ""
	c.mu.Unlock()

	if dispatcher != nil {
		pc := runtime.RunConfig{Reminders: reminders}
		if block, reason := run.DispatchUserPromptSubmit(ctx, dispatcher, &pc, deps, text); block {
			c.mu.Lock()
			c.running = false
			c.status.Running = false
			c.mu.Unlock()
			c.addNotice("Prompt blocked by hook: "+reason, true)
			c.emitStatus()
			return nil
		}
		reminders = pc.Reminders
	}

	agentCtx.Messages = append(agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	})
	c.addItem(Item{
		ID:   runID + "-user",
		Kind: ItemUser,
		Text: text,
		Done: true,
	})
	if err := c.persist(); err != nil {
		c.addNotice("Session save failed: "+err.Error(), true)
	}
	c.emitStatus()

	runCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.remind = reminders
	c.mu.Unlock()
	c.runWG.Add(1)
	go c.run(runCtx, runID, text)
	return nil
}

// Cancel interrupts the run in flight. It is a no-op when nothing runs.
func (c *Controller) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// buildConfig assembles one run's configuration from the live state. It takes
// the lock only to copy fields: the gate classifier is invoked during assembly
// and takes the same lock, so holding it here would deadlock.
func (c *Controller) buildConfig() runtime.RunConfig {
	c.mu.Lock()
	live := c.live
	reg := c.reg
	remind := c.remind
	budget := c.budget
	sessionID := c.header.ID
	dispatcher := c.dispatcher
	deps := c.hookDeps
	schedule := c.schedule
	extensions := c.opt.Extensions
	prompt := c.prompt
	creds := c.creds
	model, providerName := live.Model, live.ProviderName
	thinking, prov := live.ThinkingLevel, live.Provider
	window := live.ContextWindow
	compactionSettings := live.CompactionSettings()
	c.mu.Unlock()

	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         model,
			Provider:      providerName,
			ThinkingLevel: thinking,
			Stream:        provider.StreamFnFromProvider(prov),
			GetAPIKey:     creds.GetAPIKey,
			ContextWindow: window,
			Compaction:    compactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg},
		},
		Reminders:     remind,
		Budget:        budget,
		SessionID:     sessionID,
		MemoryRoot:    run.MemoryRootFromTools(c.opt.Tools),
		CompactionLog: compactionlog.Log,
		Checkpoint:    c.checkpointMidRun,
	}
	run.InstallExtensions(&cfg, extensions, prompt)
	gates := run.NewGates(run.GatesOpts{
		Trust:      c.trust,
		Cwd:        c.cwd,
		Perms:      c.perms,
		Notes:      c.notes,
		Sandboxed:  run.SandboxGate(),
		Classifier: c.reviewer,
		Confirm:    c.confirmApproval,
		ExtraRoots: run.ReadableExtraRoots(),
	})
	gates.Install(&cfg, dispatcher, deps)
	if schedule != nil {
		cfg.GetFollowUpMessages = schedule.FollowUpMessages
	}
	return cfg
}

// reviewer builds the classifier for one chain from the live model.
func (c *Controller) reviewer() judge.Classifier {
	c.mu.Lock()
	live := c.live
	creds := c.creds
	sessionID := c.header.ID
	cwd := c.cwd
	trusted := c.trust == nil || c.trust.IsTrusted(c.cwd) || c.opt.Approve
	c.mu.Unlock()
	return run.NewReviewer(live.Model, live.ProviderName, live.Provider, creds, sessionID, cwd, trusted)
}

// run drives one turn to completion and folds its events into the transcript.
func (c *Controller) run(ctx context.Context, runID, prompt string) {
	defer c.runWG.Done()

	c.mu.Lock()
	notes := c.notes
	agentCtx := c.agentCtx
	c.preTurnLeaf = c.curLeaf
	c.preTurnLabel = prompt
	c.mu.Unlock()

	if notes != nil {
		notes.Set(func(n judge.Note) { c.addNote(n) })
		defer notes.Set(nil)
	}

	cfg := c.buildConfig()
	stream := runtime.StartRun(ctx, agentCtx, cfg)

	turn := 0
	assistantID := ""
	assistantText := ""
	thinkingID := ""
	thinkingText := ""
	tools := map[string]string{} // tool call id -> item id

	resetTurn := func() {
		turn++
		assistantID, assistantText = "", ""
		thinkingID, thinkingText = "", ""
	}
	upsertAssistant := func(text string) {
		if text == "" || text == assistantText {
			return
		}
		assistantText = text
		if assistantID == "" {
			assistantID = fmt.Sprintf("%s-a-%d", runID, turn)
			c.addItem(Item{ID: assistantID, Kind: ItemAssistant, Text: text})
			return
		}
		c.updateItem(assistantID, func(it *Item) { it.Text = text })
	}
	upsertThinking := func(text string) {
		if text == "" || text == thinkingText {
			return
		}
		thinkingText = text
		if thinkingID == "" {
			thinkingID = fmt.Sprintf("%s-t-%d", runID, turn)
			c.addItem(Item{ID: thinkingID, Kind: ItemThinking, Text: text})
			return
		}
		c.updateItem(thinkingID, func(it *Item) { it.Text = text })
	}
	upsertTool := func(callID, name string, fn func(*ToolState)) string {
		id, ok := tools[callID]
		if !ok {
			id = runID + "-tool-" + callID
			tools[callID] = id
			st := &ToolState{ID: callID, Name: name, Running: true}
			if fn != nil {
				fn(st)
			}
			c.addItem(Item{ID: id, Kind: ItemTool, Text: name, Tool: st})
			return id
		}
		c.updateItem(id, func(it *Item) {
			if it.Tool == nil {
				it.Tool = &ToolState{ID: callID, Name: name}
			}
			if fn != nil {
				fn(it.Tool)
			}
			it.Text = it.Tool.Name
			it.Failed = it.Tool.Error
			it.Done = !it.Tool.Running
		})
		return id
	}

	_, err := runtime.DrainStream(ctx, stream, runtime.StreamHandler{
		OnEvent: func(ev agentcore.AgentEvent) {
			switch e := ev.(type) {
			case agentcore.MessageUpdateEvent:
				msg, ok := e.Message.(agentcore.AssistantMessage)
				if !ok {
					return
				}
				upsertAssistant(agentcore.ContentToText(msg.Content))
				for _, block := range msg.Content {
					if th, ok := block.(agentcore.ThinkingContent); ok {
						upsertThinking(th.Thinking)
					}
				}
			case agentcore.ToolExecutionStartEvent:
				args := decodeArgsAny(e.Args)
				upsertTool(e.ToolCallID, e.ToolName, func(st *ToolState) {
					st.Input, st.Running = args, true
				})
			case agentcore.ToolExecutionUpdateEvent:
				out := agentcore.ContentToText(e.PartialResult.Content)
				upsertTool(e.ToolCallID, e.ToolName, func(st *ToolState) {
					st.Output = out
				})
			case agentcore.ToolExecutionEndEvent:
				out := agentcore.ContentToText(e.Result.Content)
				upsertTool(e.ToolCallID, e.ToolName, func(st *ToolState) {
					st.Output, st.Running, st.Error = out, false, e.IsError
					st.Details = cloneAnyMap(decodeArgsAny(e.Result.Details))
				})
			case agentcore.ContextUsageEvent:
				c.mu.Lock()
				c.status.ContextUsed = e.Tokens
				if e.Window > 0 {
					c.status.ContextLimit = e.Window
				}
				c.mu.Unlock()
				c.emitStatus()
			case agentcore.TelemetryEvent:
				c.mu.Lock()
				c.status.ContextUsed = e.ContextTokens
				if e.ContextWindow > 0 {
					c.status.ContextLimit = e.ContextWindow
				}
				c.mu.Unlock()
			case agentcore.CompactionStartEvent:
				c.addNotice(fmt.Sprintf("Compacting conversation (%s)…", e.Reason), false)
			case agentcore.CompactionEvent:
				c.mu.Lock()
				c.compacted = true
				c.mu.Unlock()
				if e.ErrorMessage != "" {
					c.addNotice(fmt.Sprintf("Context compaction failed: %s", e.ErrorMessage), true)
				} else {
					c.addNotice(fmt.Sprintf("Context compacted: %d → %d tokens (%d summarized, %d kept)",
						e.TokensBefore, e.TokensAfter, e.SummarizedCount, e.KeptCount), false)
				}
			case agentcore.ProviderNoticeEvent:
				if e.Kind == "retry" {
					c.addNotice(fmt.Sprintf("Provider stream interrupted (%s); retry %d of %d…",
						e.Reason, e.Attempt, e.MaxAttempts), false)
				}
			case agentcore.SubAgentProgressEvent:
				// A sub-agent's progress is not part of the conversation; the
				// task tool's own card already shows its running state.
			}
		},
		OnText: func(delta string) {
			if delta == "" {
				return
			}
			upsertAssistant(assistantText + delta)
		},
		OnTurnEnd: func(msg agentcore.AssistantMessage, results []agentcore.ToolResultMessage, stall runtime.TurnStall) {
			if text := agentcore.ContentToText(msg.Content); text != "" {
				upsertAssistant(text)
			}
			if assistantID != "" {
				c.updateItem(assistantID, func(it *Item) { it.Done = true })
			}
			if thinkingID != "" {
				c.updateItem(thinkingID, func(it *Item) { it.Done = true })
			}
			switch msg.StopReason {
			case agentcore.StopReasonError:
				reason := strings.TrimSpace(msg.ErrorMessage)
				if reason == "" {
					reason = "the provider returned an error with no message"
				}
				c.addNotice("Error: "+reason, true)
			case agentcore.StopReasonAborted:
				c.addNotice("Interrupted.", false)
			default:
				if stall.Count > 0 {
					c.addNotice(runtime.StallNote(stall), false)
				} else if len(results) == 0 && !msg.HasVisibleOutput() && assistantText == "" {
					c.addNotice("The model ended this turn with no answer and no tool call.", true)
				}
			}
			resetTurn()
		},
	})

	c.mu.Lock()
	c.running, c.cancel = false, nil
	c.status.Running = false
	c.mu.Unlock()
	if err != nil {
		c.addNotice("Run failed: "+err.Error(), true)
		c.setError(err)
	}
	if perr := c.persist(); perr != nil {
		c.addNotice("Session save failed: "+perr.Error(), true)
	}
	c.mu.Lock()
	c.status.ContextUsed = estimateTokens(c.agentCtx.Messages)
	errText := c.status.Error
	c.mu.Unlock()
	c.emit(EventRunEnd, func(ev *Event) { ev.Error = errText })
	c.emitStatus()
}

// checkpointMidRun persists the conversation at turn boundaries so a crash
// loses at most the round in flight.
func (c *Controller) checkpointMidRun(_ context.Context, agentCtx *agentcore.AgentContext, compacted bool) {
	if compacted {
		c.mu.Lock()
		c.compacted = true
		c.mu.Unlock()
	}
	_ = c.persist()
}

func estimateTokens(msgs agentcore.MessageList) int {
	if len(msgs) == 0 {
		return 0
	}
	return compaction.EstimateContextTokens(msgs).Tokens
}

func decodeArgsAny(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case []byte:
		return decodeArgs(t)
	case string:
		return decodeArgs([]byte(t))
	}
	return nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// SetPermissionMode switches the live approval mode.
func (c *Controller) SetPermissionMode(ctx context.Context, name string) error {
	mode, ok := permissions.Parse(name)
	if !ok {
		return fmt.Errorf("unknown permission mode %q", name)
	}
	c.perms.Set(mode)
	c.mu.Lock()
	c.status.Permission = mode.String()
	c.mu.Unlock()
	c.emitStatus()
	return nil
}

// PermissionModes lists the selectable modes in display order.
func PermissionModes() []string {
	return []string{
		permissions.ReadOnly.String(),
		permissions.Ask.String(),
		permissions.Auto.String(),
		permissions.FullAccess.String(),
	}
}
