package appsession

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sessionLock is one writer's claim on a session file. Two front-ends must not
// append to the same JSONL tree: a lost branch cursor would either duplicate
// entries or fork history silently.
type sessionLock struct {
	path string
}

// acquireSessionLock creates <session>.lock beside the session file. A lock
// left by a dead process is reclaimed; a live holder is refused.
func acquireSessionLock(dir, id string) (*sessionLock, error) {
	if dir == "" || id == "" {
		return nil, errors.New("session lock: empty store or session id")
	}
	path := filepath.Join(dir, id+".lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return &sessionLock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("session lock: %w", err)
		}
		pid, stale := lockHolder(path)
		if !stale && processAlive(pid) {
			return nil, fmt.Errorf("session %s is open in another process (pid %d)", id, pid)
		}
		_ = os.Remove(path)
	}
	return nil, fmt.Errorf("session %s is locked", id)
}

// lockHolder reads a lock file. It reports the pid and whether the lock is
// stale (unreadable, or older than a day, which outlives any real turn).
func lockHolder(path string) (pid int, stale bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, true
	}
	if time.Since(st.ModTime()) > 24*time.Hour {
		return 0, true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, true
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, true
	}
	return pid, false
}

func (l *sessionLock) release() {
	if l == nil || l.path == "" {
		return
	}
	_ = os.Remove(l.path)
	l.path = ""
}
