package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const auditMaxBytes = 1 << 20

type auditEntry struct {
	Time    string `json:"ts"`
	Session string `json:"session,omitempty"`
	Seam    string `json:"seam"`
	Tool    string `json:"tool,omitempty"`
	Rule    string `json:"rule"`
	Action  string `json:"action"`
	Hash    string `json:"hash"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
}

// auditor appends one JSON object per finding. It never records the secret, the
// surrounding text, or the tool argument.
type auditor struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func newAuditor(path string) *auditor {
	path = expandHome(strings.TrimSpace(path))
	if path == "" {
		return nil
	}
	a := &auditor{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if info, err := f.Stat(); err == nil {
		a.size = info.Size()
	}
	a.f = f
	return a
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func (a *auditor) log(e auditEntry) {
	if a == nil {
		return
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return
	}
	if a.size+int64(len(line)) > auditMaxBytes {
		a.rotate()
		if a.f == nil {
			return
		}
	}
	if _, err := a.f.Write(line); err != nil {
		return
	}
	a.size += int64(len(line))
}

func (a *auditor) rotate() {
	_ = a.f.Close()
	a.f = nil
	_ = os.Remove(a.path + ".1")
	_ = os.Rename(a.path, a.path+".1")
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	a.f = f
	a.size = 0
}
