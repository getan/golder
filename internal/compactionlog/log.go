// Package compactionlog persists auto-compaction attempts for diagnosis.
//
// The TUI shows a compaction failure as a transient notice, and a retry can
// succeed moments later. Without a durable record the first error disappears
// with the process, leaving only the successful checkpoint and no way to tell
// a transient stream cut from a deterministic summarization bug. This package
// appends one JSON line per attempt to ~/.golder/logs/compaction.jsonl.
package compactionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/runtime"
)

const maxErrorBytes = 16 << 10

var mu sync.Mutex

// Log appends one compaction attempt. It is best-effort: a diagnostic write
// must never turn a successful compaction into a failed run.
func Log(attempt runtime.CompactionAttempt) {
	if attempt.Timestamp.IsZero() {
		attempt.Timestamp = time.Now().UTC()
	}
	attempt.Error = truncateError(attempt.Error)

	line, err := json.Marshal(attempt)
	if err != nil {
		return
	}
	line = append(line, '\n')

	dir := golderhome.File("logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	// One process (the agent TUI) does the writing, but a sub-agent or a second
	// golder process can share the directory. The mutex serializes this process;
	// O_APPEND plus one Write keeps each line intact across processes.
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "compaction.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}

func truncateError(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	return s[:maxErrorBytes] + "…"
}
