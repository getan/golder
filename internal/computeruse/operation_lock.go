package computeruse

import (
	"context"
	"sync"
	"sync/atomic"
)

// operationLock serializes backend use without making a canceled caller wait
// indefinitely behind an unrelated platform operation. Its zero value works.
type operationLock struct {
	once    sync.Once
	gate    chan struct{}
	waiting atomic.Int32
}

func (m *operationLock) init() {
	m.once.Do(func() {
		m.gate = make(chan struct{}, 1)
		m.gate <- struct{}{}
	})
}

func (m *operationLock) lock(ctx context.Context) error {
	return m.lockPreempt(ctx, nil)
}

func (m *operationLock) lockPreempt(ctx context.Context, preempt func()) error {
	m.init()
	m.waiting.Add(1)
	defer m.waiting.Add(-1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if preempt != nil {
		preempt()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.gate:
		if err := ctx.Err(); err != nil {
			m.unlock()
			return err
		}
		return nil
	}
}

func (m *operationLock) tryLock() bool {
	m.init()
	if m.waiting.Load() != 0 {
		return false
	}
	select {
	case <-m.gate:
		return true
	default:
		return false
	}
}

func (m *operationLock) unlock() { m.gate <- struct{}{} }
