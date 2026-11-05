package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	_ "image/png"
)

const (
	MaxFrameBytes  = 8 << 20
	MaxFramePixels = 16 << 20
)

// CaptureBackend returns only the selected target window in PNG format. The
// adapter must refuse a capture if it cannot reliably exclude other windows.
type CaptureBackend interface {
	Capture(context.Context, Target) ([]byte, error)
}

// Frame is ephemeral. It must not be put into ordinary logs or persisted as a
// session artifact without an explicit retention policy and user consent.
type Frame struct {
	ID     string
	Width  int
	Height int
	PNG    []byte
}

func (s *Session) SupportsCapture() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.backend.(CaptureBackend)
	return ok
}

func (s *Session) Capture(ctx context.Context) (Frame, error) {
	var frame Frame
	err := s.Do(ctx, false, func(ctx context.Context, backend Backend, target Target) error {
		capture, ok := backend.(CaptureBackend)
		if !ok {
			return ErrUnavailable
		}
		raw, err := capture.Capture(ctx, target)
		if err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > MaxFrameBytes {
			return errors.New("computer use frame exceeds the byte limit or is empty")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format != "png" || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxFramePixels {
			return errors.New("computer use frame is invalid or exceeds the pixel limit")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		frame = Frame{ID: hex.EncodeToString(nonce[:]), Width: cfg.Width, Height: cfg.Height, PNG: raw}
		return nil
	})
	if err != nil {
		return Frame{}, err
	}
	return frame, nil
}
