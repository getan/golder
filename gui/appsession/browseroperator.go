package appsession

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// BrowserOperator is the in-process, person-originated input seam of a native
// browser host. It is not available through a model-facing browser socket.
type BrowserOperator interface {
	SendOperatorInput(context.Context, BrowserOperatorInput) error
}

// BrowserOperatorInput contains only an exact target and a bounded UI event.
type BrowserOperatorInput struct {
	BrowserID, TabID, TargetID string
	Kind                       string
	X, Y, DeltaY               float64
	Text, Key                  string
}

type browserOperatorEntry struct{ operator BrowserOperator }

var browserOperators = struct {
	sync.Mutex
	bySocket map[string]*browserOperatorEntry
}{bySocket: make(map[string]*browserOperatorEntry)}

func browserOperatorSocket(socket string) string {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return ""
	}
	if abs, err := filepath.Abs(socket); err == nil {
		socket = abs
	}
	if real, err := filepath.EvalSymlinks(socket); err == nil {
		socket = real
	}
	return filepath.Clean(socket)
}

// RegisterBrowserOperator installs a trusted host for GUI-originated input.
// Its cleanup removes only this registration, never a replacement host.
func RegisterBrowserOperator(socket string, operator BrowserOperator) func() {
	key := browserOperatorSocket(socket)
	if key == "" || operator == nil {
		return func() {}
	}
	entry := &browserOperatorEntry{operator: operator}
	browserOperators.Lock()
	browserOperators.bySocket[key] = entry
	browserOperators.Unlock()
	return func() {
		browserOperators.Lock()
		if browserOperators.bySocket[key] == entry {
			delete(browserOperators.bySocket, key)
		}
		browserOperators.Unlock()
	}
}

func browserOperator(socket string) BrowserOperator {
	key := browserOperatorSocket(socket)
	browserOperators.Lock()
	defer browserOperators.Unlock()
	if entry := browserOperators.bySocket[key]; entry != nil {
		return entry.operator
	}
	return nil
}
