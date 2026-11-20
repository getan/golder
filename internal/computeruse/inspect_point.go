package computeruse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"time"
)

// No element handles, titles, field values or foreign-window metadata. This
// describes the current hit; it is neither a control lease nor proof of intent.
type PointInspection struct {
	Role                 string `json:"role,omitempty"`
	WindowVerified       bool   `json:"window_verified"`
	NonSensitiveVerified bool   `json:"non_sensitive_verified"`
	EditableVerified     bool   `json:"editable_verified"`
	PointUnobscured      bool   `json:"point_unobscured"`
	PointTile            string `json:"point_tile"`
	InputBound           bool   `json:"input_bound"`
	InputKind            string `json:"input_kind,omitempty"`
	Note                 string `json:"note,omitempty"`
}

type pointInspector interface {
	InspectPoint(context.Context, Target, string, float64, float64) (PointInspection, error)
}

func (s *Session) SupportsPointInspection() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.backend.(pointInspector)
	return ok
}

func pointMask(point [2]float64) uint16 {
	x, y := point[0], point[1]
	if !(x >= 0 && x < 1 && y >= 0 && y < 1) {
		return 0
	}
	return 1 << (int(y*tileCount)*tileCount + int(x*tileCount))
}

func sameRegions(before, after []byte, mask uint16) bool {
	if mask == 0 || len(before) != tileCount*tileCount*sha256.Size || len(before) != len(after) {
		return false
	}
	for tile := 0; tile < tileCount*tileCount; tile++ {
		i := tile * sha256.Size
		if mask&(1<<tile) != 0 && !bytes.Equal(before[i:i+sha256.Size], after[i:i+sha256.Size]) {
			return false
		}
	}
	return true
}

func comparePointTile(before, after []byte, point [2]float64) string {
	mask := pointMask(point)
	if mask == 0 || len(before) != tileCount*tileCount*sha256.Size || len(after) != len(before) {
		return "unknown"
	}
	if sameRegions(before, after, mask) {
		return "unchanged"
	}
	return "changed"
}

type pointRegionsKey struct{}

func (s *Session) InspectPoint(ctx context.Context, id string, x, y float64, contextTiles ...int) (PointInspection, error) {
	var mask uint16
	if len(contextTiles) > tileCount*tileCount {
		return PointInspection{}, ErrNotAllowed
	}
	for _, tile := range contextTiles {
		if tile < 0 || tile >= tileCount*tileCount {
			return PointInspection{}, ErrNotAllowed
		}
		mask |= 1 << tile
	}
	ctx = context.WithValue(ctx, pointRegionsKey{}, mask)
	var out PointInspection
	err := s.Do(ctx, false, func(ctx context.Context, b Backend, t Target) error {
		probe, ok := b.(pointInspector)
		if !ok {
			return ErrUnavailable
		}
		s.mu.Lock()
		lease := s.frames[id]
		seq := s.sequence
		s.mu.Unlock()
		if lease.token == "" || lease.targetID != t.ID || lease.sequence != seq || time.Since(lease.created) > frameLeaseLifetime {
			return ErrStaleObservation
		}
		if !(x >= 0 && x < float64(lease.width) && y >= 0 && y < float64(lease.height)) {
			return ErrNotAllowed
		}
		var err error
		out, err = probe.InspectPoint(ctx, t, lease.token, x/float64(lease.width), y/float64(lease.height))
		return err
	})
	if err != nil {
		return PointInspection{}, err
	}
	return out, nil
}
