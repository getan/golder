package agenttool

// Which pane is a browser, and which browser is that pane.
//
// A foreground program named "golder" is not proof of anything: any shell can
// run it, and after the browser exits the pane's foreground program changes
// anyway. A socket path is not proof either: it can be reused by a second
// browser. So the pane golder started is recorded with the exact pane id, the
// control socket it owns and the browserId that endpoint reported at launch,
// and close requires all three to still line up before it sends a quit.
//
// The record lives in memory for this process and, best effort, in the
// workspace metadata so a later golder session can still identify the pane. A
// missing record is never papered over with a guess: the caller is told to
// inspect the pane or to force a hard close.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// browserPaneRecord is the identity of one pane golder started as a browser.
type browserPaneRecord struct {
	Root     string `json:"-"`
	Name     string `json:"name"`
	WindowID string `json:"window_id"`
	Socket   string `json:"socket"`
	// Program is the launch command's base name (golder's own binary, or
	// kitty-browse). The daemon reports the foreground program by that name, so
	// a versioned or renamed install is still recognized.
	Program   string `json:"program,omitempty"`
	BrowserID string `json:"browser_id,omitempty"`
	Embedded  bool   `json:"embedded,omitempty"`
}

var browserPaneRegistry = struct {
	mu       sync.RWMutex
	byWindow map[string]browserPaneRecord
}{byWindow: map[string]browserPaneRecord{}}

func rememberBrowserPane(record browserPaneRecord) {
	if record.WindowID == "" {
		return
	}
	browserPaneRegistry.mu.Lock()
	browserPaneRegistry.byWindow[record.WindowID] = record
	browserPaneRegistry.mu.Unlock()
}

// managedBrowserPane returns the recorded identity for one exact pane id: first
// from this process, then from the workspace metadata a previous session wrote.
// A pane id is only meaningful inside its own daemon session, which is why the
// lookup is always keyed by the full id and never by name or title.
func managedBrowserPane(cwd, windowID string) (browserPaneRecord, bool) {
	if windowID == "" {
		return browserPaneRecord{}, false
	}
	browserPaneRegistry.mu.RLock()
	record, ok := browserPaneRegistry.byWindow[windowID]
	browserPaneRegistry.mu.RUnlock()
	if ok {
		if record.Root == "" {
			record.Root = cwd
		}
		return record, true
	}
	if strings.TrimSpace(cwd) == "" {
		return browserPaneRecord{}, false
	}
	dir := browserStateDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return browserPaneRecord{}, false
	}
	if len(entries) > 256 {
		entries = entries[:256]
	}
	for _, entry := range entries {
		name := entry.Name()
		// A record is read from a regular file only: a directory, a socket or a
		// link named "*.browser.json" is not a record golder wrote.
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".browser.json") {
			continue
		}
		meta, err := readBrowserMetadata(filepath.Join(dir, name))
		if err != nil || meta.WindowID != windowID {
			continue
		}
		// The record is repository content: it has to name a browser, a socket and
		// a profile golder could have created here before close acts on its name.
		if checkBrowserRecord(cwd, meta) != nil {
			continue
		}
		return browserPaneRecord{
			Root: cwd, Name: meta.Name, WindowID: meta.WindowID, Socket: meta.Socket,
			Program: meta.Program, BrowserID: meta.BrowserID, Embedded: meta.Embedded,
		}, true
	}
	return browserPaneRecord{}, false
}

// forgetBrowserPane drops the bookkeeping for a pane that is gone, including
// the metadata that made its socket discoverable — but only when that metadata
// still describes this pane, so a newer browser with the same name keeps its
// record.
func forgetBrowserPane(cwd, windowID string) {
	if windowID == "" {
		return
	}
	browserPaneRegistry.mu.Lock()
	record, inMemory := browserPaneRegistry.byWindow[windowID]
	delete(browserPaneRegistry.byWindow, windowID)
	browserPaneRegistry.mu.Unlock()
	if inMemory && record.Root != "" {
		cwd = record.Root
	}
	if !inMemory {
		// Nothing in this process, but a previous session may have written the
		// record this close just used.
		record, inMemory = managedBrowserPane(cwd, windowID)
	}
	if !inMemory || strings.TrimSpace(cwd) == "" || record.Name == "" {
		return
	}
	// The name came from this process or from a record a checkout could have
	// written, and it becomes a file name below: a name that is not a plain file
	// name must never reach a path this function removes.
	if checkBrowserName(record.Name) != nil {
		return
	}
	path := browserMetadataPath(cwd, record.Name)
	if meta, err := readBrowserMetadata(path); err == nil && meta.WindowID == windowID {
		removeBrowserMetadata(cwd, record.Name)
	}
}
