package computeruse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// SemanticBackend may act only on the backend snapshot and ref passed here;
// it must reject stale nodes and revalidate the target immediately before an
// OS action. It must not treat an arbitrary model ref as a system handle.
type SemanticBackend interface {
	SupportsAction(string) bool
	Act(context.Context, Target, string, string, string, string) error
}

type snapshotLease struct {
	publicID  string
	backendID string
	refs      map[string]bool
}

func (s *Session) SupportsSemantic(action string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backend, ok := s.backend.(SemanticBackend)
	return ok && backend.SupportsAction(action)
}

// Observe validates the authorized target and binds the returned refs to a
// fresh, unpredictable session-owned snapshot identity.
func (s *Session) Observe(ctx context.Context) (Snapshot, error) {
	var result Snapshot
	err := s.Do(ctx, false, func(ctx context.Context, backend Backend, target Target) error {
		observer, ok := backend.(SnapshotBackend)
		if !ok {
			return ErrUnavailable
		}
		s.mu.Lock()
		sequence := s.sequence
		s.snapshot = nil
		s.mu.Unlock()
		backendID, elements, truncated, err := observer.Snapshot(ctx, target)
		if err != nil {
			return err
		}
		if backendID == "" {
			return errors.New("computer use backend returned an empty snapshot identity")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		result = BoundedSnapshot(hex.EncodeToString(nonce[:]), elements)
		result.Truncated = result.Truncated || truncated
		refs := make(map[string]bool, len(result.Elements))
		for _, element := range result.Elements {
			if element.Ref == "" || refs[element.Ref] {
				return errors.New("computer use backend returned an empty or duplicate ref")
			}
			refs[element.Ref] = true
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.sequence != sequence || !s.status.Active || s.status.Paused || s.status.Target.ID != target.ID {
			return ErrStaleObservation
		}
		s.snapshot = &snapshotLease{publicID: result.ID, backendID: backendID, refs: refs}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

// Act executes a supported semantic action only against a current observed
// ref. The lease is consumed even on failure so callers must observe again.
func (s *Session) Act(ctx context.Context, snapshotID, ref, action, text string) error {
	if action != "click" && action != "fill" {
		return ErrNotAllowed
	}
	if action == "click" && text != "" {
		return ErrNotAllowed
	}
	if len(text) > 4096 {
		return errors.New("computer use input exceeds the size limit")
	}
	return s.Do(ctx, true, func(ctx context.Context, backend Backend, target Target) error {
		performer, ok := backend.(SemanticBackend)
		if !ok {
			return ErrUnavailable
		}
		if !performer.SupportsAction(action) {
			return ErrUnavailable
		}
		s.mu.Lock()
		lease := s.snapshot
		if lease == nil || snapshotID == "" || lease.publicID != snapshotID || !lease.refs[ref] {
			s.mu.Unlock()
			return ErrStaleObservation
		}
		s.snapshot = nil
		s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return performer.Act(ctx, target, lease.backendID, ref, action, text)
	})
}
