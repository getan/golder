// Package history persists the global, append-only prompt history shared by
// golder's interactive front-ends (the TUI for now). It mirrors Codex's
// message-history store: one JSON object per line in
// $GOLDER_HOME/history.jsonl, appended with a single write so concurrent
// processes never interleave partial lines, capped by size so the file cannot
// grow without bound. The front-end reads the newest entries once (lazily,
// on first use) and keeps them in memory for ↑/↓ browsing and Ctrl+R search
// across sessions.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// FileName is the history file inside the golder home directory.
const FileName = "history.jsonl"

// MaxEntries bounds what Load returns: the newest N entries. The on-disk file
// can hold more (up to maxBytes); the reader only ever holds this window.
const MaxEntries = 500

// maxBytes caps the file. Append trims past it, keeping the newest lines down
// to trimRatio of the cap so trimming is not repeated on every write. It is a
// var so tests can lower it.
var maxBytes int64 = 4 << 20 // 4 MiB

// trimRatio is the fraction of maxBytes the file is trimmed back to.
const trimRatio = 0.8

// Entry is one persisted prompt-history record. Text is the fully expanded
// prompt as submitted (paste placeholders resolved), so recalling and
// resubmitting it is faithful.
type Entry struct {
	TS        int64  `json:"ts"`
	SessionID string `json:"session_id,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Text      string `json:"text"`
}

// Append adds one entry to the history file at path, creating it (and its
// parent directory) when needed. The record is marshaled first and written to
// an O_APPEND descriptor in one Write, so concurrent appenders do not
// interleave partial lines; the file is mode 0600. When the file exceeds
// maxBytes it is rewritten to keep the newest lines (trimRatio of the cap).
// Appending to a path whose directory cannot be resolved fails.
func Append(path string, e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return err
	}
	info, statErr := f.Stat()
	if err := f.Close(); err != nil {
		return err
	}
	if statErr == nil && info.Size() > maxBytes {
		return trim(path)
	}
	return nil
}

// Load returns up to max of the newest entries in chronological (oldest-first)
// order. A missing file is an empty history, not an error; malformed lines are
// skipped (best-effort read — a corrupted line must not hide the rest).
func Load(path string, max int) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Prompts can be long; raise the line cap well above bufio's 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var entries []Entry
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if max > 0 && len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	return entries, nil
}

// trim rewrites path keeping the newest whole lines whose total size fits
// trimRatio of maxBytes. The rewrite goes through a temp file and an atomic
// rename so a reader never sees a half-written history.
func trim(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Split into lines (keeping each line's terminator) and keep the newest
	// suffix under the target size.
	target := int64(float64(maxBytes) * trimRatio)
	var start int
	for start < len(data) && int64(len(data)-start) > target {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 {
			start = len(data)
			break
		}
		start += nl + 1
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data[start:], 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
