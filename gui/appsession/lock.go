package appsession

import (
	"errors"

	"github.com/getan/golder/internal/session"
)

// sessionLock is one writer's claim on a session file. Two front-ends must not
// append to the same JSONL tree: a lost branch cursor would either duplicate
// entries or fork history silently.
type sessionLock struct {
	writer *session.WriterLock
}

// acquireSessionLock uses the shared session package's OS-backed lock, so GUI,
// TUI, REPL and headless runs all contend on the same writer lease.
func acquireSessionLock(dir, id string) (*sessionLock, error) {
	store, err := session.NewStore(dir)
	if err != nil {
		return nil, err
	}
	writer, err := store.AcquireWriter(id, "GUI")
	if err != nil {
		return nil, err
	}
	return &sessionLock{writer: writer}, nil
}

func acquireSessionAccess(dir, id string) (lock *sessionLock, readOnly bool, holder session.WriterInfo, err error) {
	lock, err = acquireSessionLock(dir, id)
	if err == nil {
		return lock, false, session.WriterInfo{}, nil
	}
	var inUse *session.SessionInUseError
	if errors.As(err, &inUse) {
		return nil, true, inUse.Holder, nil
	}
	return nil, false, session.WriterInfo{}, err
}

func writerView(info session.WriterInfo) SessionWriterView {
	return SessionWriterView{Frontend: info.Frontend, PID: info.PID, Since: info.StartedAt}
}

func (l *sessionLock) release() {
	if l == nil || l.writer == nil {
		return
	}
	l.writer.Release()
	l.writer = nil
}
