package tui

// /rewind for the TUI: like the REPL's command, it rolls the working tree back
// to before an earlier turn (replaying the file-snapshot journal) and moves the
// conversation leaf to the same point, returning code and dialogue to an earlier
// state at once. The snapshot recorder is shared with the file tools; each
// completed turn commits one restore point (see commitRewindPoint).

import (
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/session"
)

// rewindLabel derives a short one-line description of a turn from its prompt, for
// display in the /rewind list. It collapses whitespace and truncates so the list
// stays scannable.
func rewindLabel(prompt string) string {
	label := strings.Join(strings.Fields(prompt), " ")
	const max = 60
	if len(label) > max {
		label = label[:max-1] + "…"
	}
	return label
}

// commitRewindPoint groups the just-finished turn's file mutations (if any) into
// a restore point tagged with the leaf the turn descended from. It is a no-op
// when the snapshot recorder is absent (file tools disabled).
func (s *runSession) commitRewindPoint() {
	if s.snap == nil {
		return
	}
	s.snap.Commit(s.preTurnLeaf, rewindLabel(s.preTurnLabel))
	s.preTurnLabel = ""
}

// rewindPoints returns the accumulated restore points, oldest first.
func (s *runSession) rewindPoints() []agenttool.RestorePoint {
	if s.snap == nil {
		return nil
	}
	return s.snap.Points()
}

// rewindTo restores the files and the conversation to before the n-th restore
// point (1-based, as listed): the snapshot journal is replayed and the active
// leaf moves to the branch the turn descended from (an empty leaf resets to an
// empty conversation). It returns the restored paths and any warnings.
func (s *runSession) rewindTo(n int) (restored, warnings []string, msgs []agentcore.Message, err error) {
	if s.snap == nil {
		return nil, nil, nil, fmt.Errorf("rewind is unavailable (file tools are disabled)")
	}
	leafID, restored, warnings, err := s.snap.Restore(n - 1)
	if err != nil {
		return nil, nil, nil, err
	}
	msgs, ok := s.rewindConversation(leafID)
	if !ok {
		warnings = append(warnings, "restore point's conversation node is no longer in the tree; files were restored but the conversation was left unchanged")
		return restored, warnings, s.agentCtx.Messages, nil
	}
	return restored, warnings, msgs, nil
}

// rewindConversation moves the active leaf back to leafID and rebuilds the
// context from that leaf's root→leaf path (the same mechanism /tree uses). An
// empty leafID resets to an empty conversation (the turn was the session's
// first). The rebuilt history is returned for transcript reseeding.
func (s *runSession) rewindConversation(leafID string) ([]agentcore.Message, bool) {
	if leafID == "" {
		s.agentCtx.Messages = nil
		s.curLeaf = ""
		s.persisted = 0
		return nil, true
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil {
		return nil, false
	}
	path := session.PathToLeaf(entries, leafID)
	if len(path) == 0 {
		return nil, false
	}
	msgs := make(agentcore.MessageList, len(path))
	for i, e := range path {
		msgs[i] = e.Message
	}
	s.agentCtx.Messages = msgs
	s.curLeaf = leafID
	s.persisted = len(msgs)
	return msgs, true
}
