package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	errWriterLocked      = errors.New("session writer lock is held")
	errWriterUnsupported = errors.New("session writer locks are unsupported on this platform")
)

type WriterInfo struct {
	PID       int       `json:"pid"`
	Frontend  string    `json:"frontend"`
	StartedAt time.Time `json:"started_at"`
}

type SessionInUseError struct {
	ID     string
	Holder WriterInfo
}

func (e *SessionInUseError) Error() string {
	if e == nil {
		return "session is open in another Golder process"
	}
	detail := strings.TrimSpace(e.Holder.Frontend)
	if detail == "" {
		detail = "Golder"
	}
	if e.Holder.PID > 0 {
		detail += fmt.Sprintf(" · PID %d", e.Holder.PID)
	}
	if !e.Holder.StartedAt.IsZero() {
		detail += " · since " + e.Holder.StartedAt.Local().Format("15:04")
	}
	return fmt.Sprintf("session %s is open in another Golder process (%s); only one writer is allowed — close it or choose a different session", e.ID, detail)
}

func IsSessionInUse(err error) bool {
	var target *SessionInUseError
	return errors.As(err, &target)
}

type WriterLock struct {
	file *os.File
	info WriterInfo
	once sync.Once
}

// AcquireWriter takes the process-lifetime writer lease for id. The lock is an
// operating-system advisory lock on a stable file: Unix uses flock and Windows
// uses LockFileEx. The adjacent JSON is display metadata only, never authority.
func (s *Store) AcquireWriter(id, frontend string) (*WriterLock, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, errors.New("session writer lock: store is unavailable")
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, id+".lock")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("session writer lock is a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session writer lock: open: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("session writer lock: permissions: %w", err)
	}
	if err := lockWriterFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWriterLocked) {
			return nil, &SessionInUseError{ID: id, Holder: readWriterInfo(path + ".info")}
		}
		return nil, fmt.Errorf("session writer lock: %w", err)
	}
	lock := &WriterLock{file: f, info: WriterInfo{PID: os.Getpid(), Frontend: strings.TrimSpace(frontend), StartedAt: time.Now().UTC()}}
	if lock.info.Frontend == "" {
		lock.info.Frontend = "Golder"
	}
	if err := writeWriterInfo(path+".info", lock.info); err != nil {
		lock.Release()
		return nil, err
	}
	return lock, nil
}

func (l *WriterLock) Info() WriterInfo {
	if l == nil {
		return WriterInfo{}
	}
	return l.info
}

func (l *WriterLock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.file == nil {
			return
		}
		_ = unlockWriterFile(l.file)
		_ = l.file.Close()
		l.file = nil
	})
}

func writeWriterInfo(path string, info WriterInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-writer-info-*")
	if err != nil {
		return fmt.Errorf("session writer metadata: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("session writer metadata: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("session writer metadata: %w", err)
	}
	return nil
}

func readWriterInfo(path string) WriterInfo {
	f, err := os.Open(path)
	if err != nil {
		return WriterInfo{}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return WriterInfo{}
	}
	var info WriterInfo
	if json.Unmarshal(raw, &info) != nil {
		return WriterInfo{}
	}
	return info
}
