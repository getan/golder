package agenttool

import (
	"os"
	"sync"
)

// The desktop GUI stores search keys outside the environment. A process-local
// override lets WebSearchTool read them without exporting secrets to every
// child process; a missing key still falls back to the environment, which keeps
// the CLI/TUI behavior unchanged.
var (
	searchEnvMu sync.RWMutex
	searchEnvFn func(string) string
)

// SetSearchEnv installs the key lookup used when a WebSearchTool has no
// injected getenv. Nil restores the environment-only default.
func SetSearchEnv(fn func(string) string) {
	searchEnvMu.Lock()
	searchEnvFn = fn
	searchEnvMu.Unlock()
}

func defaultSearchEnv(name string) string {
	searchEnvMu.RLock()
	fn := searchEnvFn
	searchEnvMu.RUnlock()
	if fn != nil {
		if value := fn(name); value != "" {
			return value
		}
	}
	return os.Getenv(name)
}
