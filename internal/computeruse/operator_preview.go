package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
)

type operatorPreviewLease struct {
	id    string
	frame Frame
	done  chan struct{}
}

// InstallOperatorPreview is called by the local host before it exposes the
// session to an agent. A pending preview blocks every model-facing operation.
func (s *Session) InstallOperatorPreview(preview OperatorPreview) error {
	if s == nil || preview.ID == "" || len(preview.PNG) == 0 || len(preview.PNG) > MaxFrameBytes {
		return ErrNotAllowed
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(preview.PNG))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > MaxFramePixels {
		return errors.New("invalid operator-only preview")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Active || s.status.Paused || s.backend == nil || s.operatorPreview != nil {
		return ErrNotAllowed
	}
	if _, ok := s.backend.(OperatorPreviewBackend); !ok {
		return ErrUnavailable
	}
	s.operatorPreview = &operatorPreviewLease{id: preview.ID, frame: Frame{Width: config.Width, Height: config.Height, PNG: bytes.Clone(preview.PNG)}, done: make(chan struct{})}
	s.operatorPreviewDone = s.operatorPreview.done
	s.operatorPreviewSequence = s.sequence
	return nil
}

func (s *Session) operatorPreviewFrame() (Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Active || s.status.Paused || s.operatorPreview == nil {
		return Frame{}, ErrUnavailable
	}
	frame := s.operatorPreview.frame
	frame.PNG = bytes.Clone(frame.PNG)
	return frame, nil
}

func (s *Session) finishOperatorPreview(ctx context.Context, confirm bool) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if !s.status.Active || s.status.Paused || s.operatorPreview == nil || s.backend == nil {
		s.mu.Unlock()
		return ErrNotAllowed
	}
	lease, target, sequence, backend := s.operatorPreview, s.status.Target, s.sequence, s.backend
	s.mu.Unlock()
	var err error
	if confirm {
		gate, ok := backend.(OperatorPreviewBackend)
		if !ok {
			return ErrUnavailable
		}
		err = gate.ConfirmOperatorPreview(ctx, target, lease.id)
	} else {
		gate, ok := backend.(OperatorPreviewDiscarder)
		if !ok {
			return ErrUnavailable
		}
		err = gate.DiscardOperatorPreview(ctx, target, lease.id)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.status.Active || s.status.Paused || s.sequence != sequence || s.operatorPreview != lease {
		return ErrStaleObservation
	}
	s.operatorPreview = nil
	close(lease.done)
	return nil
}

// WaitOperatorPreview lets the host delay exposing the agent tool until the
// authenticated operator pane confirms or explicitly declines the preview.
func (s *Session) WaitOperatorPreview(ctx context.Context) error {
	s.mu.Lock()
	done, sequence := s.operatorPreviewDone, s.operatorPreviewSequence
	if done == nil {
		s.mu.Unlock()
		return ErrNotAllowed
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.status.Active || s.operatorPreview != nil || s.sequence != sequence {
			return ErrNotActive
		}
		return nil
	}
}
