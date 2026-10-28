package provider

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// Only inspect the original protocol item. Display summaries are deliberately
// excluded: neither summary text nor a fabricated thought can replace a signed
// thinking block. Both gateway spellings retain their original JSON on replay.
func reasoningItemText(raw json.RawMessage) string {
	var item struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return ""
	}
	var text strings.Builder
	for _, part := range item.Content {
		switch part.Type {
		case "reasoning_text":
			text.WriteString(part.Text)
		case "thinking":
			text.WriteString(part.Thinking)
		}
	}
	return text.String()
}

func reasoningItemsText(items []json.RawMessage) string {
	var text strings.Builder
	for _, raw := range items {
		text.WriteString(reasoningItemText(raw))
	}
	return text.String()
}

func hasThinkingContent(content agentcore.ContentList) bool {
	for _, block := range content {
		if thinking, ok := block.(agentcore.ThinkingContent); ok && thinking.Thinking != "" {
			return true
		}
	}
	return false
}

// Completed items remain authoritative. Fill missing finalized items, and keep
// the richer item_done when the terminal event repeats only an empty summary.
// Never concatenate/rewrite opaque fields or replay the same item twice.
func mergeFinalizedReasoning(items, finalized []json.RawMessage) []json.RawMessage {
	items = append([]json.RawMessage(nil), items...)
	find := func(want json.RawMessage) int {
		for i, raw := range items {
			if sameReasoningItem(raw, want) {
				return i
			}
		}
		return -1
	}
	for i, done := range finalized {
		if index := find(done); index >= 0 {
			if !reasoningHasText([]json.RawMessage{items[index]}) && reasoningHasText([]json.RawMessage{done}) {
				items[index] = done
			}
			continue
		}
		// Fill a missing earlier item before its next shared anchor, rather
		// than appending it after a later item the terminal event did retain.
		at := len(items)
		for _, next := range finalized[i+1:] {
			if index := find(next); index >= 0 {
				at = index
				break
			}
		}
		items = append(items, nil)
		copy(items[at+1:], items[at:])
		items[at] = done
	}
	return items
}

func sameReasoningItem(a, b json.RawMessage) bool {
	var first, second struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(a, &first) != nil || json.Unmarshal(b, &second) != nil {
		return false
	}
	if first.ID != "" || second.ID != "" {
		return first.ID != "" && first.ID == second.ID
	}
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}
