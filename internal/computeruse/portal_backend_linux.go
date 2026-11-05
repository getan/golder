//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// PrepareOperatorPreview starts a single-window portal stream. The returned
// pixels are operator-only; Capture refuses them until explicitly confirmed.
func (b *linuxATSPIBackend) PrepareOperatorPreview(ctx context.Context, target Target) (OperatorPreview, error) {
	if err := b.Validate(ctx, target, false); err != nil {
		return OperatorPreview{}, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return OperatorPreview{}, ErrUnavailable
	}
	b.portalSequence++
	sequence, old := b.portalSequence, b.portal
	b.portal, b.portalTarget, b.portalCandidate, b.portalApproved = nil, "", "", false
	b.x11Ready = false
	b.mu.Unlock()
	if old != nil {
		old.close()
	}
	portal, err := openLinuxPortal(ctx)
	if err != nil {
		return OperatorPreview{}, err
	}
	installed := false
	defer func() {
		if !installed {
			portal.close()
		}
	}()
	data, err := portal.frame(ctx)
	if err != nil {
		return OperatorPreview{}, err
	}
	if err := b.Validate(ctx, target, false); err != nil {
		return OperatorPreview{}, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return OperatorPreview{}, err
	}
	id := hex.EncodeToString(nonce[:])
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OperatorPreview{}, err
	}
	if b.closed || b.portalSequence != sequence || portal.revoked.Load() {
		return OperatorPreview{}, ErrTargetLost
	}
	b.portal, b.portalTarget, b.portalCandidate = portal, target.ID, id
	installed = true
	return OperatorPreview{ID: id, PNG: data}, nil
}

func (b *linuxATSPIBackend) ConfirmOperatorPreview(ctx context.Context, target Target, id string) error {
	if err := b.Validate(ctx, target, false); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed || b.portal == nil || b.portal.revoked.Load() || b.portalTarget != target.ID || id == "" || b.portalCandidate != id {
		return ErrStaleObservation
	}
	b.portalCandidate = ""
	b.portalApproved = true
	return nil
}

func (b *linuxATSPIBackend) CaptureAvailable() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && b.portal != nil && b.portalApproved && !b.portal.revoked.Load()
}

func (b *linuxATSPIBackend) DiscardOperatorPreview(ctx context.Context, target Target, id string) error {
	if err := b.Validate(ctx, target, false); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed || b.portal == nil || b.portalTarget != target.ID || id == "" || b.portalCandidate != id {
		b.mu.Unlock()
		return ErrStaleObservation
	}
	portal := b.portal
	b.portalSequence++
	b.portal, b.portalTarget, b.portalCandidate, b.portalApproved = nil, "", "", false
	b.x11Ready = false
	b.mu.Unlock()
	portal.close()
	return ctx.Err()
}

// Capture can only return the stream after the operator has matched its local
// preview to the separately selected AT-SPI target.
func (b *linuxATSPIBackend) Capture(ctx context.Context, target Target) ([]byte, error) {
	if err := b.Validate(ctx, target, false); err != nil {
		return nil, err
	}
	b.mu.Lock()
	portal, approved := b.portal, b.portalApproved && b.portalTarget == target.ID
	b.mu.Unlock()
	if portal == nil || !approved {
		return nil, errors.New("window preview requires local portal selection and confirmation")
	}
	data, err := portal.frame(ctx)
	if err != nil {
		return nil, err
	}
	if err := b.Validate(ctx, target, false); err != nil {
		return nil, err
	}
	b.mu.Lock()
	valid := !b.closed && b.portal == portal && b.portalApproved && b.portalTarget == target.ID && !portal.revoked.Load()
	b.mu.Unlock()
	if !valid {
		return nil, ErrTargetLost
	}
	return data, nil
}
