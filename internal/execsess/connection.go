package execsess

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

// SessionConnection adapts one managed TTY to MyGo terminal's Conn contract.
// It mirrors retained output plus live chunks and writes keystrokes back to the
// same PTY. Close only detaches this reader.
type SessionConnection struct {
	session *Session
	id      uint64
	stream  <-chan []byte

	mu      sync.Mutex
	pending *bytes.Reader
	done    chan struct{}
	once    sync.Once
}

func newSessionConnection(session *Session, id uint64, stream <-chan []byte, initial []byte) *SessionConnection {
	return &SessionConnection{
		session: session, id: id, stream: stream,
		pending: bytes.NewReader(initial), done: make(chan struct{}),
	}
}

func (c *SessionConnection) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if c.pending != nil && c.pending.Len() > 0 {
			n, err := c.pending.Read(p)
			c.mu.Unlock()
			return n, err
		}
		c.mu.Unlock()

		select {
		case chunk := <-c.stream:
			c.mu.Lock()
			c.pending = bytes.NewReader(chunk)
			c.mu.Unlock()
		case <-c.session.exitedCh:
			select {
			case chunk := <-c.stream:
				c.mu.Lock()
				c.pending = bytes.NewReader(chunk)
				c.mu.Unlock()
				continue
			default:
				return 0, io.EOF
			}
		case <-c.done:
			return 0, io.EOF
		}
	}
}

func (c *SessionConnection) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, errors.New("execsess: tty connection is closed")
	default:
	}
	if err := c.session.SendInput(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize is discovered by terminal.Options.Conn and forwarded to the PTY.
func (c *SessionConnection) Resize(cols, rows int) error {
	return c.session.Resize(cols, rows)
}

func (c *SessionConnection) Close() error {
	c.once.Do(func() {
		c.session.mu.Lock()
		delete(c.session.subscribers, c.id)
		c.session.mu.Unlock()
		close(c.done)
	})
	return nil
}
