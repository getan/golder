package agenttool

// Capabilities are the session's live answer to "what can this agent actually
// do right now": is a browser listening, is the tuios binary on PATH.
//
// Two rules keep the answer honest without making the tool list flap:
//
//  1. Presence is decided once, when the tool set is built. A tool that exists
//     stays registered for the session, so the model's tool list is stable.
//  2. Content follows the capability. A tool trims its action enum when the
//     backend is missing and grows it back when the backend arrives. Providers
//     encode the schema on every request, so the next turn carries the change.
//
// The only event that creates a capability mid-session is workspace create
// starting a browser, and that is exactly where the socket override is set.
// Nothing polls: a background probe would flip the schema (and invalidate the
// provider's prompt cache) for a change nobody asked for.

import (
	"os/exec"
	"sync"

	"github.com/getan/golder/internal/browserctl"
)

var (
	browserSocketMu sync.RWMutex
	// browserSocketOverride is the socket a workspace preset started this
	// session. The environment variable still wins: a person who exported it is
	// pointing at the browser they chose.
	browserSocketOverride string

	tuiosLookPath = exec.LookPath
)

// browserSocket resolves the control socket for this process.
func browserSocket() string {
	if s := browserctl.SocketFromEnv(); s != "" {
		return s
	}
	browserSocketMu.RLock()
	defer browserSocketMu.RUnlock()
	return browserSocketOverride
}

// setBrowserSocket records the socket a preset just started, so the browser
// tool can use it without a restart.
func setBrowserSocket(path string) {
	browserSocketMu.Lock()
	browserSocketOverride = path
	browserSocketMu.Unlock()
}

// hasTuiosBinary reports whether diagrams and dashboards can work. LookPath is
// a handful of stats along PATH; it is cheaper than caching and keeps a binary
// installed mid-session from staying invisible.
func hasTuiosBinary() bool {
	_, err := tuiosLookPath("tuios")
	return err == nil
}
