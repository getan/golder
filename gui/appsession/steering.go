package appsession

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/runtime"
)

// queuedPrompt owns the already-read attachment content so a queued message
// does not change if a selected file is edited or removed before delivery.
type queuedPrompt struct {
	id              string
	display         string
	text            string
	attachmentLabel string
	content         agentcore.ContentList
	browserWaitRun  string // trusted continuation reservation, never decoded from user input
}

// statusWithQueueLocked returns an independent view while c.mu is held.
func (c *Controller) statusWithQueueLocked() Status {
	status := c.status
	status.QueuedMessages = make([]string, 0, len(c.steerQueue))
	status.QueuedDetails = make([]QueuedMessage, 0, len(c.steerQueue))
	for _, prompt := range c.steerQueue {
		status.QueuedMessages = append(status.QueuedMessages, prompt.display)
		status.QueuedDetails = append(status.QueuedDetails, QueuedMessage{ID: prompt.id, Text: prompt.text, Display: prompt.display})
	}
	return status
}

func (c *Controller) enqueueSteerLocked(content agentcore.ContentList, display string) {
	c.enqueuePreparedLocked(queuedPrompt{content: content, display: display, text: display})
}

func (c *Controller) enqueuePreparedLocked(prompt queuedPrompt) {
	c.steerSequence++
	prompt.id = fmt.Sprintf("steer-%d", c.steerSequence)
	c.steerQueue = append(c.steerQueue, prompt)
}

// RemoveQueued removes an input only while it remains in the pending FIFO.
func (c *Controller) RemoveQueued(id string) error {
	c.mu.Lock()
	if c.closed || c.readOnly {
		c.mu.Unlock()
		return errors.New("session is closed or read-only")
	}
	for i, prompt := range c.steerQueue {
		if prompt.id == id {
			c.steerQueue = slices.Delete(c.steerQueue, i, i+1)
			c.mu.Unlock()
			c.emitStatus()
			return nil
		}
	}
	c.mu.Unlock()
	return errors.New("queued message was already delivered or removed")
}

// EditQueued replaces only the prompt text, retaining snapshotted attachments.
func (c *Controller) EditQueued(id, text string) error {
	text = strings.TrimSpace(text)
	c.mu.Lock()
	if c.closed || c.readOnly {
		c.mu.Unlock()
		return errors.New("session is closed or read-only")
	}
	for i := range c.steerQueue {
		p := &c.steerQueue[i]
		if p.id != id {
			continue
		}
		content := append(agentcore.ContentList(nil), p.content...)
		if p.text != "" && len(content) > 0 {
			content = content[1:]
		}
		if text == "" && len(content) == 0 {
			c.mu.Unlock()
			return errors.New("queued message cannot be empty")
		}
		if text != "" {
			content = append(agentcore.ContentList{agentcore.NewTextContent(text)}, content...)
		}
		p.text, p.content = text, content
		p.display = text
		if p.attachmentLabel != "" {
			if p.display != "" {
				p.display += "\n\n"
			}
			p.display += p.attachmentLabel
		}
		c.mu.Unlock()
		c.emitStatus()
		return nil
	}
	c.mu.Unlock()
	return errors.New("queued message was already delivered or removed")
}

func (c *Controller) popSteerLocked() (queuedPrompt, bool) {
	if len(c.steerQueue) == 0 {
		return queuedPrompt{}, false
	}
	prompt := c.steerQueue[0]
	c.steerQueue[0] = queuedPrompt{}
	c.steerQueue = c.steerQueue[1:]
	return prompt, true
}

// abortPreparedPrompt restores a previously queued input when its submit
// context expires before it enters the conversation.
func (c *Controller) abortPreparedPrompt(prompt queuedPrompt) {
	c.mu.Lock()
	if prompt.id != "" && !c.closed {
		c.steerQueue = append([]queuedPrompt{prompt}, c.steerQueue...)
	}
	c.running, c.status.Running = false, false
	c.mu.Unlock()
	c.emitStatus()
}

// pullSteering runs on the agent loop goroutine. It sends a user-message marker
// through that same event stream before returning each message, preserving the
// transcript order relative to the preceding assistant and tool events.
func (c *Controller) pullSteering(ctx context.Context, stream *runtime.LoopEventStream) []agentcore.AgentMessage {
	c.mu.Lock()
	batch := c.steerQueue
	c.steerQueue = nil
	c.mu.Unlock()
	messages := make([]agentcore.AgentMessage, 0, len(batch))
	for i, prompt := range batch {
		if ctx.Err() != nil {
			c.mu.Lock()
			c.steerQueue = append(append([]queuedPrompt(nil), batch[i:]...), c.steerQueue...)
			c.mu.Unlock()
			break
		}
		c.mu.Lock()
		c.steerDelivered = append(c.steerDelivered, prompt)
		c.mu.Unlock()
		message := agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: prompt.content}
		if err := stream.Emit(ctx, agentcore.MessageStartEvent{Message: message}); err != nil {
			c.mu.Lock()
			// A failed Emit cannot have reached the consumer. Earlier delivered
			// markers may have been consumed, but this one is still last.
			last := len(c.steerDelivered) - 1
			if last >= 0 && c.steerDelivered[last].id == prompt.id {
				c.steerDelivered[last] = queuedPrompt{}
				c.steerDelivered = c.steerDelivered[:last]
			}
			c.steerQueue = append(append([]queuedPrompt(nil), batch[i:]...), c.steerQueue...)
			c.mu.Unlock()
			break
		}
		messages = append(messages, message)
	}
	if len(batch) > 0 {
		c.emitStatus()
	}
	return messages
}

// showDeliveredSteer runs on the event-draining goroutine, in stream order.
func (c *Controller) showDeliveredSteer() {
	c.mu.Lock()
	if len(c.steerDelivered) == 0 {
		c.mu.Unlock()
		return
	}
	prompt := c.steerDelivered[0]
	c.steerDelivered[0] = queuedPrompt{}
	c.steerDelivered = c.steerDelivered[1:]
	c.mu.Unlock()
	c.addItem(Item{ID: prompt.id, Kind: ItemUser, Text: prompt.display, Images: userImages(prompt.content), Done: true})
}

// prepareQueuedPrompt starts a fallback turn after the preceding run has fully
// drained. Unlike an in-loop steer, it passes through the normal prompt hook.
func (c *Controller) prepareQueuedPrompt(ctx context.Context, prompt queuedPrompt) bool {
	c.mu.Lock()
	dispatcher, deps, reminders := c.dispatcher, c.hookDeps, c.remind
	c.mu.Unlock()
	if dispatcher != nil && ctx.Err() == nil {
		cfg := runtime.RunConfig{Reminders: reminders}
		if block, reason := run.DispatchUserPromptSubmit(ctx, dispatcher, &cfg, deps, prompt.display); block && ctx.Err() == nil {
			c.addNotice("Prompt blocked by hook: "+reason, true)
			return false
		}
		reminders = cfg.Reminders
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	if ctx.Err() != nil {
		c.steerQueue = append([]queuedPrompt{prompt}, c.steerQueue...)
		c.mu.Unlock()
		c.emitStatus()
		return false
	}
	c.agentCtx.Messages = append(c.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser, Content: prompt.content,
	})
	c.remind = reminders
	c.mu.Unlock()
	c.addItem(Item{ID: prompt.id, Kind: ItemUser, Text: prompt.display, Images: userImages(prompt.content), Done: true})
	if err := c.persist(); err != nil {
		c.addNotice("Session save failed: "+err.Error(), true)
	}
	c.emitStatus()
	return true
}
