// This file implements FileSnapshotRecorder, the edit-rewind journal backing the
// /rewind command. Before the write and edit tools mutate a file they call
// Record(absPath), which captures the file's prior content (or notes that it did
// not exist). Snapshots accumulate per turn; Commit groups the turn's snapshots
// into a RestorePoint tagged with the conversation leaf that preceded the turn.
// Restore replays a suffix of the restore points in reverse to roll the working
// tree back to an earlier state, mirroring Claude Code's Esc-Esc rewind. The
// journal is in-memory and scoped to the running session; only golder's own
// write/edit tools are captured (arbitrary bash edits are not).
package agenttool

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// snapshotMaxBytes caps how large a file may be for its prior content to be held
// in the rewind journal. A file above this is still recorded (so rewind knows it
// changed) but its content is not retained, and rewind reports it as skipped
// rather than clobbering it with stale bytes.
const snapshotMaxBytes = 16 * 1024 * 1024

// fileSnapshot is the pre-mutation state of a single file: its content before the
// first write/edit of a turn, or a marker that it did not yet exist (so rewind
// deletes it). TooLarge marks a file that exceeded snapshotMaxBytes, whose
// content was not retained.
type fileSnapshot struct {
	Path     string
	Existed  bool
	TooLarge bool
	Content  []byte
}

// RestorePoint is one turn's worth of file snapshots plus the conversation leaf
// that preceded the turn. Rewinding to it restores every file to its Snapshots
// state and moves the active conversation leaf back to LeafID.
type RestorePoint struct {
	Seq       int
	Time      time.Time
	LeafID    string
	Label     string
	Snapshots []fileSnapshot
}

// FileSnapshotRecorder captures prior file content before write/edit mutations
// and groups it into per-turn RestorePoints. Its methods are safe for concurrent
// use so parallel tool calls within a turn can record without racing.
type FileSnapshotRecorder struct {
	mu      sync.Mutex
	pending map[string]fileSnapshot // absolute path -> first snapshot this turn
	order   []string                // first-touch order within the turn
	points  []RestorePoint
	nextSeq int
}

// NewFileSnapshotRecorder returns an empty recorder ready to record the first
// turn's mutations.
func NewFileSnapshotRecorder() *FileSnapshotRecorder {
	return &FileSnapshotRecorder{pending: map[string]fileSnapshot{}, nextSeq: 1}
}

// Record captures the current on-disk state of absPath before it is mutated. Only
// the first call for a given path within a turn is retained, so the snapshot
// reflects the state at the turn's start (later mutations in the same turn are
// rolled back to that same baseline). A nil recorder is a no-op, so tools can
// hold an always-safe optional handle.
func (r *FileSnapshotRecorder) Record(absPath string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.pending[absPath]; seen {
		return
	}
	snap := fileSnapshot{Path: absPath}
	info, err := os.Stat(absPath)
	switch {
	case err != nil:
		// Treat any stat error (including not-exist) as "did not exist": rewind will
		// delete the file created this turn.
		snap.Existed = false
	case info.IsDir():
		// A directory is never written by the file tools; skip content capture.
		snap.Existed = true
		snap.TooLarge = true
	case info.Size() > snapshotMaxBytes:
		snap.Existed = true
		snap.TooLarge = true
	default:
		data, readErr := os.ReadFile(absPath)
		if readErr != nil {
			snap.Existed = true
			snap.TooLarge = true
		} else {
			snap.Existed = true
			snap.Content = data
		}
	}
	r.pending[absPath] = snap
	r.order = append(r.order, absPath)
}

// Commit closes the current turn: if any files were recorded it appends a
// RestorePoint tagged with leafID (the conversation leaf before the turn) and
// label (a short description, e.g. the user prompt), then clears the pending
// buffer. A turn that mutated no files creates no restore point. It reports
// whether a restore point was created.
func (r *FileSnapshotRecorder) Commit(leafID, label string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return false
	}
	snaps := make([]fileSnapshot, 0, len(r.order))
	for _, p := range r.order {
		snaps = append(snaps, r.pending[p])
	}
	r.points = append(r.points, RestorePoint{
		Seq:       r.nextSeq,
		Time:      time.Now().UTC(),
		LeafID:    leafID,
		Label:     label,
		Snapshots: snaps,
	})
	r.nextSeq++
	r.pending = map[string]fileSnapshot{}
	r.order = nil
	return true
}

// Points returns a copy of the committed restore points, oldest first.
func (r *FileSnapshotRecorder) Points() []RestorePoint {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RestorePoint, len(r.points))
	copy(out, r.points)
	return out
}

// Restore rolls the working tree back to the state before the restore point at
// index idx (0-based into the Points slice). It replays that point and every
// later point in reverse, restoring each file's prior content (or deleting files
// that did not exist), then drops those points from the journal so the next
// rewind starts from the new tip. It returns the conversation leaf to switch to
// (the target point's LeafID), the list of restored file paths, and any
// non-fatal warnings (e.g. files skipped because they were too large or a
// restore write failed).
func (r *FileSnapshotRecorder) Restore(idx int) (leafID string, restored []string, warnings []string, err error) {
	if r == nil {
		return "", nil, nil, fmt.Errorf("no restore points")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx < 0 || idx >= len(r.points) {
		return "", nil, nil, fmt.Errorf("restore point %d out of range (have %d)", idx+1, len(r.points))
	}
	leafID = r.points[idx].LeafID

	// A file touched across several turns must end at its OLDEST (pre-target)
	// baseline. Iterate points oldest→newest and keep only the first snapshot seen
	// for each path, so the earliest baseline is the one applied.
	applied := map[string]bool{}
	for i := idx; i < len(r.points); i++ {
		for _, s := range r.points[i].Snapshots {
			if applied[s.Path] {
				continue
			}
			applied[s.Path] = true
			if w := applySnapshot(s); w != "" {
				warnings = append(warnings, w)
				continue
			}
			restored = append(restored, s.Path)
		}
	}

	r.points = r.points[:idx]
	if len(r.points) > 0 {
		r.nextSeq = r.points[len(r.points)-1].Seq + 1
	} else {
		r.nextSeq = 1
	}
	return leafID, restored, warnings, nil
}

// applySnapshot restores one file to its recorded prior state: rewrite the prior
// content, or delete the file if it did not exist before. It returns a warning
// string when the file cannot be safely restored (too large to have retained
// content, or a filesystem error), or "" on success.
func applySnapshot(s fileSnapshot) string {
	if s.TooLarge {
		return fmt.Sprintf("%s: skipped (too large to snapshot; left unchanged)", s.Path)
	}
	if !s.Existed {
		if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Sprintf("%s: could not delete: %v", s.Path, err)
		}
		return ""
	}
	if err := os.WriteFile(s.Path, s.Content, filePerm); err != nil {
		return fmt.Sprintf("%s: could not restore: %v", s.Path, err)
	}
	return ""
}
