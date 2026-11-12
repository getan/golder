package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	_ "image/png"
	"time"
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

// CaptureAvailability lets a backend with an operator-confirmed source avoid
// advertising screenshots while its local selection has not been approved.
type CaptureAvailability interface {
	CaptureAvailable() bool
}

// FrameBoundBackend binds a capture to opaque platform geometry. ClickFrame
// must revalidate the target, geometry and sensitive controls immediately
// before dispatch. Global input additionally requires focus and occlusion
// checks; a target-scoped semantic action must verify subtree membership.
// A token alone is never authorization.
type FrameBoundBackend interface {
	CaptureBackend
	CaptureBound(context.Context, Target) ([]byte, string, error)
	ClickFrame(context.Context, Target, string, float64, float64) error
}

const maxFrameLeases = 64
const frameLeaseLifetime = 30 * time.Second

type frameLease struct {
	targetID string
	token    string
	width    int
	height   int
	sequence uint64
	created  time.Time
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
	if !ok {
		return false
	}
	if available, ok := s.backend.(CaptureAvailability); ok {
		return available.CaptureAvailable()
	}
	return true
}

func (s *Session) Capture(ctx context.Context) (Frame, error) {
	return s.capture(ctx, false)
}

// CapturePreview is for the local operator view, not model-facing screenshots.
// It does not queue behind control operations. Only an authenticated takeover
// context receives a frame lease, so the person can click the displayed image.
func (s *Session) CapturePreview(ctx context.Context) (Frame, error) {
	return s.capture(ctx, true)
}

func (s *Session) capture(ctx context.Context, preview bool) (Frame, error) {
	var frame Frame
	err := s.do(ctx, false, preview, func(ctx context.Context, backend Backend, target Target) error {
		capture, ok := backend.(CaptureBackend)
		if !ok {
			return ErrUnavailable
		}
		var raw []byte
		var token string
		var err error
		if bound, ok := backend.(FrameBoundBackend); ok {
			raw, token, err = bound.CaptureBound(ctx, target)
		} else {
			raw, err = capture.Capture(ctx, target)
		}
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
		if (!preview || isOperatorContext(ctx)) && token != "" && len(token) <= 512 {
			s.mu.Lock()
			if err := ctx.Err(); err != nil || !s.status.Active || (s.status.Paused && !isOperatorContext(ctx)) || s.status.Target.ID != target.ID {
				s.mu.Unlock()
				return ErrStaleObservation
			}
			if s.status.Capabilities.Control {
				if s.frames == nil {
					s.frames = make(map[string]frameLease)
				}
				now := time.Now()
				for id, lease := range s.frames {
					if now.Sub(lease.created) > frameLeaseLifetime {
						delete(s.frames, id)
					}
				}
				if len(s.frames) >= maxFrameLeases {
					var oldestID string
					var oldest time.Time
					for id, lease := range s.frames {
						if oldestID == "" || lease.created.Before(oldest) {
							oldestID, oldest = id, lease.created
						}
					}
					delete(s.frames, oldestID)
				}
				s.frames[frame.ID] = frameLease{targetID: target.ID, token: token, width: cfg.Width, height: cfg.Height, sequence: s.sequence, created: now}
			}
			s.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return Frame{}, err
	}
	return frame, nil
}
