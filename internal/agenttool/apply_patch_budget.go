package agenttool

import (
	"context"
	"fmt"

	"github.com/getan/golder/internal/patch"
)

// apply_patch is the one tool that both reads a whole tree of old content and
// writes it back. These limits keep one call's working set bounded. They are
// deliberately process-wide: a batch barrier only orders calls inside ONE
// assistant message, while sub-agents and session operations can invoke the
// same tool concurrently.
const (
	applyPatchMaxInputBytes   = 8 << 20  // the patch text itself
	applyPatchMaxWorkingBytes = 64 << 20 // old/new content, snapshots and diff
	applyPatchMaxFiles        = 512      // metadata/journal fan-out
	applyPatchMaxDiffBytes    = 512 << 10
)

// applyPatchSlot is a process-wide write lock. The worker pool separately
// limits how many tool-worker processes exist; this lock additionally guarantees
// only one patch mutates the tree at a time, including direct (non-worker) calls
// from tests, plugins, or a full-access session.
var applyPatchSlot = make(chan struct{}, 1)

func acquireApplyPatch(ctx context.Context) error {
	select {
	case applyPatchSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseApplyPatch() { <-applyPatchSlot }

// patchBudget is a conservative pre-write estimate. The old and new strings are
// charged twice: once as the Change payload and once for the diff/journal copy.
// Snapshot bytes are charged separately because they are retained for /rewind.
type patchBudget struct {
	total    int64
	snapshot int64
	files    int
}

// estimatePatchBudget counts retained bytes and unique files for one call. It is
// an upper-bound estimate, not a measurement of the allocator: the point is to
// reject a call before it can retain hundreds of MiB, not to account for every
// transient buffer.
func estimatePatchBudget(patchText string, changes []patch.Change) patchBudget {
	b := patchBudget{total: int64(len(patchText)), files: len(changes)}
	snapshotPaths := make(map[string]bool, len(changes))
	for _, ch := range changes {
		old, new := int64(len(ch.OldContent)), int64(len(ch.NewContent))
		b.total += 2*(old+new) + 256 // two live copies plus diff framing
		if old == 0 {
			continue
		}
		path := ch.OldPath
		if path == "" {
			path = ch.Path
		}
		if !snapshotPaths[path] {
			snapshotPaths[path] = true
			b.snapshot += old
		}
	}
	b.total += b.snapshot
	return b
}

func patchBudgetError(b patchBudget) string {
	return fmt.Sprintf(
		"apply_patch: this patch would retain about %d bytes across %d file(s), over the %d-byte working budget; nothing was written. Split it into smaller apply_patch calls",
		b.total, b.files, applyPatchMaxWorkingBytes)
}
