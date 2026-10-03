// This file holds PersistTurn, the session-tree persistence step shared by the
// REPL loop and the /goal autonomous loop. It reads and advances a session's
// mutable cursor state through the Host contract, so a command need not import
// the concrete replDeps aggregate to persist the tail of a turn.
package cli

import (
	"fmt"
	"io"
	"time"
)

// PersistTurn appends the messages produced since the last persist as a new
// branch descending from the host's current leaf, advancing the leaf and the
// persisted-message cursor. Growing the tree with AppendBranch (rather than a
// full linear Save) is what lets a later /tree leaf-switch fork the on-disk
// history instead of clobbering it. If nothing new was produced it is a no-op:
// rewriting the file would regenerate entry ids and flatten the tree.
func PersistTurn(out io.Writer, h Host) {
	agentCtx := h.AgentCtx()
	// Automatic compaction during a turn rewrites Messages into a summary + recent
	// tail, shrinking the slice below the persisted cursor. The append-a-tail
	// branch model no longer holds (the prefix changed and Messages[persisted:]
	// would be out of range), so re-save the flattened context linearly and reset
	// the branch cursor to the new leaf, mirroring the /compact handler.
	if h.Persisted() > len(agentCtx.Messages) {
		header := h.Header()
		header.UpdatedAt = time.Now().UTC()
		if err := h.Store().Save(header, agentCtx.Messages); err != nil {
			fmt.Fprintf(out, "golder: session save failed: %v\n", err)
			return
		}
		h.SetPersisted(len(agentCtx.Messages))
		h.SetCurLeaf("")
		if _, entries, err := h.Store().LoadEntries(header.ID); err == nil && len(entries) > 0 {
			h.SetCurLeaf(entries[len(entries)-1].ID)
		}
		return
	}
	tail := agentCtx.Messages[h.Persisted():]
	if len(tail) == 0 {
		return
	}
	header := h.Header()
	header.UpdatedAt = time.Now().UTC()
	leaf, err := h.Store().AppendBranch(header, h.CurLeaf(), tail)
	if err != nil {
		fmt.Fprintf(out, "golder: session save failed: %v\n", err)
		return
	}
	h.SetCurLeaf(leaf)
	h.SetPersisted(len(agentCtx.Messages))
}
