package mygoui

import (
	"strings"

	"github.com/getan/golder/gui/appsession"
)

// The virtual list does not lay out offscreen markdown, so count logical text
// lines rather than pretending to know its wrapped visual line count.
func transcriptTextLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func transcriptTail(items []appsession.Item) appsession.Item {
	if len(items) == 0 {
		return appsession.Item{}
	}
	last := items[len(items)-1]
	return appsession.Item{ID: last.ID, Kind: last.Kind, Text: last.Text}
}

func (s *transcriptWindow) noteAddedItem(item appsession.Item) {
	if s == nil || !s.started || s.follow || s.scroll.MaxY <= 0 {
		return
	}
	lines := 1
	switch item.Kind {
	case appsession.ItemUser, appsession.ItemAssistant, appsession.ItemNotice:
		lines = transcriptTextLines(item.Text)
	}
	s.unseen = min(s.unseen+lines, 999999)
}

func (s *transcriptWindow) noteUpdatedItem(previous, current appsession.Item) {
	if previous.Kind != current.Kind || (current.Kind != appsession.ItemAssistant && current.Kind != appsession.ItemNotice) {
		return
	}
	s.noteUpdatedText(current.ID, previous.Text, current.Text)
}

// Expanded thinking and tool cards can grow while their rows are offscreen.
// A collapsed exploration group must not count its members' hidden output.
func (a *App) noteExpandedTranscriptUpdate(previous, current appsession.Item) {
	if a.list == nil || !a.list.started || a.list.follow || a.list.scroll.MaxY <= 0 || previous.Kind != current.Kind {
		return
	}
	key, old, next := current.ID, previous.Text, current.Text
	switch current.Kind {
	case appsession.ItemThinking:
		if !a.thinkingOpen[current.ID] {
			return
		}
	case appsession.ItemTool:
		if !a.toolOpen[current.ID] || previous.Tool == nil || current.Tool == nil {
			return
		}
		for _, row := range a.projectedTranscriptRows() {
			for _, member := range row.members {
				if member.key == current.ID {
					if !a.toolOpen["explore:"+row.key] {
						return
					}
					key = row.key
				}
			}
		}
		// Match the tool card's display limit: omitted output is not a new
		// visible transcript line.
		old, next = truncate(previous.Tool.Output, 12000), truncate(current.Tool.Output, 12000)
	default:
		return
	}
	a.list.noteUpdatedText(key, old, next)
}

func (s *transcriptWindow) noteUpdatedText(id, previous, current string) {
	if s == nil || !s.started || s.follow || s.scroll.MaxY <= 0 {
		return
	}
	if row, ok := s.index[id]; ok && row < s.last {
		return // An update above the visible viewport is not new content below it.
	}
	if previous == current || !strings.HasPrefix(current, previous) {
		return
	}
	added := current[len(previous):]
	lines := strings.Count(added, "\n")
	if previous == "" {
		lines++
	}
	if lines == 0 && s.unseen == 0 {
		lines = 1 // A streaming fragment can extend an existing offscreen line.
	}
	s.unseen = min(s.unseen+lines, 999999)
}

// A background session does not render events. Compare the saved tail when the
// session is restored, then count only entries appended after it. If the old
// tail disappeared (for example after compaction), avoid inventing a count.
func (s *transcriptWindow) noteRestoredItems(tail appsession.Item, items []appsession.Item, index map[string]int) {
	if s == nil || tail.ID == "" {
		return
	}
	i, ok := index[tail.ID]
	if !ok || i < 0 || i >= len(items) {
		return
	}
	s.noteUpdatedItem(tail, items[i])
	for _, item := range items[i+1:] {
		s.noteAddedItem(item)
	}
}
