// Package computeruse owns the platform-independent authorization and
// lifecycle of a single locally resolved desktop window. Platform adapters must
// fail closed when they cannot prove the target and input conditions.
package computeruse

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrUnavailable = errors.New("computer use backend unavailable")
	ErrNotActive   = errors.New("computer use session is not active")
	ErrNotAllowed  = errors.New("computer use action is not authorized")
	ErrPaused      = errors.New("computer use is paused; the user must resume")
	ErrTargetLost  = errors.New("computer use target changed or is unavailable")
	ErrPreviewBusy = errors.New("computer use preview skipped while another operation is running")
)

// Target is an opaque identity returned by a platform backend. The local host
// resolves or confirms it; model-supplied process identifiers never select it.
type Target struct {
	ID            string
	Application   string
	ApplicationID string // platform identity, never a model-supplied selector
	Window        string
	Detail        string // operator-facing disambiguation; never a model selector
}

type Capabilities struct {
	Observe bool
	Control bool
}

type Status struct {
	Active         bool
	Paused         bool
	PendingPreview bool
	Target         Target
	Capabilities   Capabilities
}

// Backend is implemented by a platform-specific adapter. Validate must check
// the identity and conditions needed for the requested action immediately
// before execution. A successful capture does not imply safe input control.
// Backends must honor context cancellation and recheck it before injecting
// input; cancellation cannot undo an input already delivered to the OS.
type Backend interface {
	Validate(context.Context, Target, bool) error
	Close() error
}

// SnapshotBackend is an optional semantic observation capability. Backends
// must keep refs scoped to the current target and snapshot.
type SnapshotBackend interface {
	Snapshot(context.Context, Target) (string, []Element, bool, error)
}

// Session serializes lifecycle transitions with target validation and backend
// operations. Only the operator-facing host may call Start or Resume.
type Session struct {
	mu                      sync.Mutex
	opMu                    operationLock
	backend                 Backend
	status                  Status
	cancel                  context.CancelFunc
	previewCancel           context.CancelFunc
	sequence                uint64
	snapshot                *snapshotLease
	frames                  map[string]frameLease
	operatorPreview         *operatorPreviewLease
	operatorPreviewDone     chan struct{}
	operatorPreviewSequence uint64
}

func New(backend Backend) *Session { return &Session{backend: backend} }

// Start installs a locally resolved target and capabilities. Starting a second
// target requires stopping the old session and resolving the new target.
func (s *Session) Start(ctx context.Context, target Target, caps Capabilities) error {
	if s == nil {
		return ErrUnavailable
	}
	if target.ID == "" || !caps.Observe {
		return ErrNotAllowed
	}
	if err := s.opMu.lock(ctx); err != nil {
		return err
	}
	defer s.opMu.unlock()
	s.mu.Lock()
	if s.backend == nil {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if s.status.Active {
		s.mu.Unlock()
		return errors.New("computer use target is already active; stop it before selecting another")
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	validationCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	sequence, backend := s.sequence, s.backend
	s.mu.Unlock()
	err := backend.Validate(validationCtx, target, false)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	if s.sequence != sequence {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil {
		if retryableValidationFailure(err) {
			return noInputValidationError(err)
		}
		return fmt.Errorf("%w: %w", ErrTargetLost, err)
	}
	s.operatorPreviewDone = nil
	s.status = Status{Active: true, Target: target, Capabilities: caps}
	return nil
}

func (s *Session) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.PendingPreview = s.operatorPreview != nil
	return status
}

func (s *Session) SupportsSnapshot() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.backend.(SnapshotBackend)
	return ok
}

// Do serializes one authorized backend operation and pauses if the platform
// cannot validate the pinned target. Callers must not invoke backend input by
// bypassing this method.
func (s *Session) Do(ctx context.Context, control bool, operation func(context.Context, Backend, Target) error) error {
	return s.do(ctx, control, false, operation)
}

// A local preview is opportunistic: it must not queue ahead of the Agent or
// change the control session's pause state if a background capture fails.
func (s *Session) do(ctx context.Context, control, preview bool, operation func(context.Context, Backend, Target) error) error {
	if s == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if preview {
		if !s.opMu.tryLock() {
			return ErrPreviewBusy
		}
	} else {
		if err := s.opMu.lockPreempt(ctx, func() {
			s.mu.Lock()
			if s.previewCancel != nil {
				s.previewCancel()
			}
			s.mu.Unlock()
		}); err != nil {
			return err
		}
	}
	defer s.opMu.unlock()
	s.mu.Lock()
	if s.backend == nil {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if !s.status.Active {
		s.mu.Unlock()
		return ErrNotActive
	}
	if s.status.Paused {
		s.mu.Unlock()
		return ErrPaused
	}
	if s.operatorPreview != nil {
		s.mu.Unlock()
		return ErrNotAllowed
	}
	if !s.status.Capabilities.Observe || (control && !s.status.Capabilities.Control) || operation == nil {
		s.mu.Unlock()
		return ErrNotAllowed
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if preview && s.opMu.waiting.Load() != 0 {
		s.mu.Unlock()
		return ErrPreviewBusy
	}
	operationCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	if preview {
		s.previewCancel = cancel
	}
	sequence, target, backend := s.sequence, s.status.Target, s.backend
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.cancel = nil
		if preview {
			s.previewCancel = nil
		}
		s.mu.Unlock()
	}()
	if err := backend.Validate(operationCtx, target, control); err != nil {
		if operationCtx.Err() != nil {
			return operationCtx.Err()
		}
		if preview {
			return err
		}
		if retryableValidationFailure(err) {
			s.mu.Lock()
			if s.sequence == sequence && s.status.Active {
				s.snapshot = nil
				s.frames = nil
			}
			s.mu.Unlock()
			return noInputValidationError(err)
		}
		s.mu.Lock()
		if s.sequence == sequence && s.status.Active {
			s.status.Paused = true
			s.sequence++
			s.snapshot = nil
			s.frames = nil
		}
		s.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrTargetLost, err)
	}
	if err := operationCtx.Err(); err != nil {
		return err
	}
	err := operation(operationCtx, backend, target)
	if operationCtx.Err() != nil {
		if control {
			return fmt.Errorf("computer use control was interrupted; its effect may be unknown, so observe before deciding what to do: %w", operationCtx.Err())
		}
		return operationCtx.Err()
	}
	if !control && !preview && retryableValidationFailure(err) {
		s.mu.Lock()
		if s.sequence == sequence && s.status.Active {
			s.snapshot = nil
			s.frames = nil
		}
		s.mu.Unlock()
		return noInputValidationError(err)
	}
	if !preview && errors.Is(err, ErrTargetLost) {
		s.mu.Lock()
		if s.sequence == sequence && s.status.Active {
			s.status.Paused = true
			s.sequence++
			s.snapshot = nil
			s.frames = nil
		}
		s.mu.Unlock()
	}
	return err
}

func (s *Session) Pause() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Active {
		s.status.Paused = true
		s.sequence++
		s.snapshot = nil
		s.frames = nil
		if s.operatorPreview != nil {
			close(s.operatorPreview.done)
			s.operatorPreview = nil
		}
		if s.cancel != nil {
			s.cancel()
		}
	}
}

// Resume must only be called from an explicit operator action. It revalidates
// the original target and never changes the scope of the original grant.
func (s *Session) Resume(ctx context.Context) error {
	if s == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return resumeValidationError(err)
	}
	// Bind the click to the pause the operator saw, before waiting behind any
	// in-flight operation. A newer Pause/Stop must invalidate a queued Resume.
	s.mu.Lock()
	if s.backend == nil {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if !s.status.Active {
		s.mu.Unlock()
		return ErrNotActive
	}
	if !s.status.Paused {
		s.mu.Unlock()
		return nil
	}
	requestedSequence := s.sequence
	s.mu.Unlock()
	if err := s.opMu.lock(ctx); err != nil {
		return resumeValidationError(err)
	}
	defer s.opMu.unlock()
	s.mu.Lock()
	if s.backend == nil {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if !s.status.Active {
		s.mu.Unlock()
		return ErrNotActive
	}
	if s.sequence != requestedSequence {
		s.mu.Unlock()
		return context.Canceled
	}
	if !s.status.Paused {
		s.mu.Unlock()
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return resumeValidationError(err)
	}
	validationCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	sequence, target, backend := s.sequence, s.status.Target, s.backend
	s.mu.Unlock()
	err := waitForWindowValidation(validationCtx, resumeSettleTimeout, windowSettleInterval, func() error {
		return backend.Validate(validationCtx, target, false)
	})
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	if s.sequence != sequence || !s.status.Active {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return resumeValidationError(err)
	}
	if err != nil {
		if retryableValidationFailure(err) {
			return resumeValidationError(err)
		}
		return fmt.Errorf("%w: %w", ErrTargetLost, err)
	}
	s.status.Paused = false
	return nil
}

func resumeValidationError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("computer use resume timed out while waiting for or validating the original window; no action was replayed: %w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return err
}

// Stop revokes the current authorization. It does not close the backend, so a
// later operator selection can create a new session.
func (s *Session) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	s.snapshot = nil
	s.frames = nil
	if s.operatorPreview != nil {
		close(s.operatorPreview.done)
		s.operatorPreview = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.status = Status{}
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.Stop()
	_ = s.opMu.lock(context.Background())
	defer s.opMu.unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = Status{}
	if s.backend == nil {
		return nil
	}
	err := s.backend.Close()
	s.backend = nil
	return err
}
