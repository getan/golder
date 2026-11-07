package terminalaccess

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// Connection is the MyGo terminal's Conn. Input from the terminal view and
// input from an Agent meet at the same lock before either reaches the PTY.
type Connection struct {
	registry   *Registry
	pane       string
	source     io.ReadWriteCloser
	onTakeover func(string)
	closing    atomic.Bool
	once       sync.Once
	closeErr   error
	eventMu    sync.Mutex
	revision   uint64
	changed    chan struct{}
	ended      bool
	pending    atomic.Bool
	viewLocked atomic.Bool
	barrierMu  sync.Mutex
	barriers   map[string]chan error
}

func NewConnection(registry *Registry, pane string, source io.ReadWriteCloser, onTakeover func(string)) (*Connection, error) {
	if registry == nil || pane == "" || source == nil {
		return nil, ErrDenied
	}
	return &Connection{registry: registry, pane: pane, source: source, onTakeover: onTakeover,
		changed: make(chan struct{}), barriers: make(map[string]chan error)}, nil
}

func (c *Connection) Read(b []byte) (int, error) {
	// MyGo calls Read again only after it has parsed the preceding bytes into
	// its screen. Publish that revision here, not before the parser sees them.
	if c.pending.Swap(false) {
		c.signal(false)
	}
	n, err := c.source.Read(b)
	if n > 0 {
		c.pending.Store(true)
	} else if err != nil {
		c.signal(true)
	}
	return n, err
}

func (c *Connection) signal(ended bool) {
	c.eventMu.Lock()
	c.revision++
	c.ended = c.ended || ended
	close(c.changed)
	c.changed = make(chan struct{})
	c.eventMu.Unlock()
}

// Revision and Wait provide event-driven output observation without polling.
// The caller must authorize reading before and after waiting.
func (c *Connection) Revision() (uint64, bool) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.revision, c.ended
}

// Observe returns an event only when there is no newer output yet.
func (c *Connection) Observe(after uint64) (uint64, bool, <-chan struct{}) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	if c.revision != after || c.ended {
		return c.revision, c.ended, nil
	}
	return c.revision, c.ended, c.changed
}

func (c *Connection) Wait(ctx context.Context, after uint64) (uint64, bool, error) {
	c.eventMu.Lock()
	if c.revision != after || c.ended {
		revision, ended := c.revision, c.ended
		c.eventMu.Unlock()
		return revision, ended, nil
	}
	changed := c.changed
	c.eventMu.Unlock()
	select {
	case <-ctx.Done():
		return after, false, ctx.Err()
	case <-changed:
		revision, ended := c.Revision()
		return revision, ended, nil
	}
}

// SetViewLocked is set only after the UI disabled local input and Barrier
// drained already-queued keystrokes. The terminal can still emit protocol
// replies while locked; those must not steal an Agent's control lease.
func (c *Connection) SetViewLocked(locked bool) { c.viewLocked.Store(locked) }
func (c *Connection) ViewLocked() bool          { return c.viewLocked.Load() }

// Barrier enqueues an unguessable marker into MyGo's input queue and consumes
// it here, never forwarding it to the PTY. A timed-out marker remains reserved
// until it arrives, so it cannot later leak into the person's shell.
func (c *Connection) Barrier(ctx context.Context, send func([]byte)) error {
	if send == nil || c.closing.Load() {
		return io.ErrClosedPipe
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	key := string(token[:])
	done := make(chan error, 1)
	c.barrierMu.Lock()
	if c.closing.Load() || len(c.barriers) >= 32 {
		c.barrierMu.Unlock()
		return errors.New("terminal: input queue is unavailable or blocked")
	}
	c.barriers[key] = done
	c.barrierMu.Unlock()
	send(token[:])
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c.barrierMu.Lock()
		if _, ok := c.barriers[key]; ok {
			c.barriers[key] = nil
		}
		c.barrierMu.Unlock()
		return ctx.Err()
	}
}

// Write is reserved for the local MyGo view and its protocol replies.
func (c *Connection) Write(b []byte) (int, error) {
	if c.closing.Load() {
		return 0, io.ErrClosedPipe
	}
	c.barrierMu.Lock()
	if done, ok := c.barriers[string(b)]; ok {
		delete(c.barriers, string(b))
		if done != nil {
			done <- nil
		}
		c.barrierMu.Unlock()
		return len(b), nil
	}
	c.barrierMu.Unlock()
	var n int
	if c.viewLocked.Load() {
		err := c.registry.SystemInput(c.pane, func() error {
			if c.closing.Load() {
				return io.ErrClosedPipe
			}
			var err error
			n, err = c.source.Write(b)
			return err
		})
		return n, err
	}
	previous, err := c.registry.UserInput(c.pane, func() error {
		if c.closing.Load() {
			return io.ErrClosedPipe
		}
		var err error
		n, err = c.source.Write(b)
		return err
	})
	if previous != "" && c.onTakeover != nil {
		c.onTakeover(previous)
	}
	return n, err
}

// AgentWrite must be called only after the tool's execution and host-control
// gates approve this exact input. Registry.Use rechecks the current lease.
func (c *Connection) AgentWrite(agent string, b []byte) (int, error) {
	return c.AgentWriteChecked(agent, b, nil)
}

// AgentWriteChecked performs an additional live-policy check while the pane's
// input lease is held, directly before writing to the underlying PTY.
func (c *Connection) AgentWriteChecked(agent string, b []byte, check func() error) (int, error) {
	if c.closing.Load() {
		return 0, io.ErrClosedPipe
	}
	var n int
	err := c.registry.Use(c.pane, agent, true, func() error {
		if c.closing.Load() {
			return io.ErrClosedPipe
		}
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		var err error
		n, err = c.source.Write(b)
		return err
	})
	return n, err
}

func (c *Connection) Resize(cols, rows int) error {
	if c.closing.Load() {
		return io.ErrClosedPipe
	}
	if s, ok := c.source.(interface{ Resize(int, int) error }); ok {
		return s.Resize(cols, rows)
	}
	return errors.New("terminal connection does not support resizing")
}

func (c *Connection) Close() error {
	c.once.Do(func() {
		c.closing.Store(true)
		c.barrierMu.Lock()
		for key, done := range c.barriers {
			if done != nil {
				done <- io.ErrClosedPipe
			}
			delete(c.barriers, key)
		}
		c.barrierMu.Unlock()
		// A process exit closes MyGo's Conn as well. Revoke input but retain
		// read grants until the pane itself is removed, so its final output can
		// still be inspected.
		c.closeErr = c.source.Close()
		c.registry.MarkExited(c.pane)
		c.signal(true)
	})
	return c.closeErr
}
