package appsession

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	cliui "github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/judge"
)

// titleOf derives a session title from its first user message.
func titleOf(items []Item) string {
	for _, it := range items {
		if it.Kind != ItemUser {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(it.Text, "\n", 2)[0])
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return line
	}
	return "New session"
}

func cloneItem(it Item) Item {
	out := it
	out.Images = append([]agentcore.ImageContent(nil), it.Images...)
	if it.Tool != nil {
		t := *it.Tool
		if it.Tool.Input != nil {
			t.Input = make(map[string]any, len(it.Tool.Input))
			for k, v := range it.Tool.Input {
				t.Input[k] = v
			}
		}
		t.Details = cloneAnyMap(it.Tool.Details)
		out.Tool = &t
	}
	if it.Note != nil {
		n := *it.Note
		out.Note = &n
	}
	return out
}

// addItem appends an item and emits it. The caller must not hold c.mu.
func (c *Controller) addItem(it Item) {
	if it.Time.IsZero() {
		it.Time = time.Now()
	}
	c.mu.Lock()
	if c.index == nil {
		c.index = map[string]int{}
	}
	c.index[it.ID] = len(c.items)
	c.items = append(c.items, it)
	if it.Kind == ItemUser && c.header.DisplayName == "" && (c.title == "" || c.title == "New session") {
		c.title = titleOf(c.items)
		c.status.Title = c.title
	}
	emit := cloneItem(it)
	c.mu.Unlock()
	c.emit(EventItemAdded, func(ev *Event) { ev.Item = &emit })
}

// updateItem applies fn to the item of id and emits the result. It reports
// whether the item exists.
func (c *Controller) updateItem(id string, fn func(*Item)) bool {
	c.mu.Lock()
	i, ok := c.index[id]
	if !ok {
		c.mu.Unlock()
		return false
	}
	fn(&c.items[i])
	it := cloneItem(c.items[i])
	c.mu.Unlock()
	c.emit(EventItemUpdated, func(ev *Event) { ev.Item = &it })
	return true
}

// addNotice appends a controller announcement.
func (c *Controller) addNotice(text string, failed bool) {
	now := time.Now()
	c.addItem(Item{
		ID:     fmt.Sprintf("notice-%d", now.UnixNano()),
		Kind:   ItemNotice,
		Text:   text,
		Done:   true,
		Failed: failed,
		Time:   now,
	})
}

// addNote records one permission-gate verdict as a transcript item.
func (c *Controller) addNote(n judge.Note) {
	// Match the TUI: routine approvals are diagnostic, while sandbox routing,
	// escapes, denials and boundary decisions remain visible.
	if n.Kind == judge.NoteApproved && !cliui.DebugEnabled() {
		return
	}
	now := time.Now()
	c.mu.Lock()
	runID := c.runID
	c.mu.Unlock()
	st := &NoteState{
		Tool:         n.Tool,
		ToolCallID:   n.ToolCallID,
		RunID:        runID,
		Origin:       n.Origin,
		Kind:         int(n.Kind),
		Level:        n.Level.String(),
		Summary:      n.Summary,
		Source:       n.Source,
		Rationale:    n.Rationale,
		Mode:         n.Mode,
		DecisionCode: n.DecisionCode,
		Granted:      !n.Kind.Failure(),
		Failure:      n.Kind.Failure(),
	}
	text := judge.FormatNote(n)
	c.addItem(Item{
		ID:     fmt.Sprintf("note-%d", now.UnixNano()),
		Kind:   ItemNote,
		Text:   text,
		Done:   true,
		Note:   st,
		Failed: st.Failure,
		Time:   now,
	})
}

// seed rebuilds the transcript from persisted history. It mirrors the TUI's
// seedTranscript: text blocks become their own items, tool calls are paired
// with their recorded results, and compaction checkpoints show as notices.
func (c *Controller) seed(history []agentcore.Message) {
	results := map[string]agentcore.ToolResultMessage{}
	for _, m := range history {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID != "" {
			results[tr.ToolCallID] = tr
		}
	}
	n := 0
	next := func(prefix string) string {
		n++
		return fmt.Sprintf("%s-%d", prefix, n)
	}
	for _, m := range history {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			text := agentcore.ContentToText(msg.Content)
			images := userImages(msg.Content)
			if text == "" && len(images) == 0 {
				continue
			}
			c.appendChild(Item{ID: next("h-user"), Kind: ItemUser, Text: text, Images: images, Done: true})
		case agentcore.AssistantMessage:
			if text := agentcore.ContentToText(msg.Content); text != "" {
				c.appendChild(Item{ID: next("h-assistant"), Kind: ItemAssistant, Text: text, Done: true})
			}
			for _, block := range msg.Content {
				if th, ok := block.(agentcore.ThinkingContent); ok && strings.TrimSpace(th.Thinking) != "" {
					c.appendChild(Item{ID: next("h-thinking"), Kind: ItemThinking, Text: th.Thinking, Done: true})
				}
			}
			for _, call := range msg.ToolCalls() {
				st := &ToolState{ID: call.ID, Name: call.Name, Input: decodeArgs(call.Arguments)}
				if res, ok := results[call.ID]; ok {
					st.Output = agentcore.ContentToText(res.Content)
					st.Details = cloneAnyMap(decodeArgsAny(res.Details))
					st.Error = res.IsError
					st.Running = false
				} else {
					st.Running = false
				}
				c.appendChild(Item{
					ID:     "h-tool-" + call.ID,
					Kind:   ItemTool,
					Text:   call.Name,
					Done:   true,
					Tool:   st,
					Failed: st.Error,
				})
			}
		case agentcore.CompactionMessage:
			summary := strings.TrimSpace(msg.Summary)
			if summary == "" {
				summary = "(empty summary)"
			}
			c.appendChild(Item{
				ID:   next("h-compaction"),
				Kind: ItemNotice,
				Text: fmt.Sprintf("Context compacted: %d tokens before. %s", msg.TokensBefore, summary),
				Done: true,
			})
		}
	}
	c.mu.Lock()
	c.title = titleOf(c.items)
	c.status.Title = c.title
	c.status.ContextUsed = estimateTokens(history)
	c.mu.Unlock()
}

// appendChild adds an item during Open, before any reader can see the state.
func (c *Controller) appendChild(it Item) {
	if it.Time.IsZero() {
		it.Time = time.Now()
	}
	c.index[it.ID] = len(c.items)
	c.items = append(c.items, it)
}

func decodeArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_raw": string(raw)}
	}
	return m
}
