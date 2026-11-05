package computeruse

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

var ErrStaleObservation = errors.New("computer use observation is stale; observe the target again")

const (
	MaxSnapshotNodes = 500
	MaxNodeTextBytes = 512
	MaxSnapshotBytes = 32 << 10
)

// Element represents only bounded, non-sensitive accessibility data. Secure
// includes password controls and nodes whose safety cannot be established;
// backends must never populate their Name or Value.
type Element struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Enabled  bool   `json:"enabled"`
	Editable bool   `json:"editable,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

type Snapshot struct {
	ID        string    `json:"id"`
	Elements  []Element `json:"elements"`
	Truncated bool      `json:"truncated,omitempty"`
}

// BoundedSnapshot sanitizes platform-provided content before it can be sent to
// a model. Ref generation and identity resolution remain backend/session owned.
func BoundedSnapshot(id string, elements []Element) Snapshot {
	result := Snapshot{ID: boundedText(id), Truncated: len(elements) > MaxSnapshotNodes || len(id) > MaxNodeTextBytes}
	if len(elements) > MaxSnapshotNodes {
		elements = elements[:MaxSnapshotNodes]
	}
	result.Elements = make([]Element, 0, len(elements))
	// Reserve space for the JSON envelope and comma separators. Counting the
	// encoded elements also accounts for escaping control characters.
	remaining := MaxSnapshotBytes - 4096
	for _, e := range elements {
		if len(e.Ref) > MaxNodeTextBytes || !utf8.ValidString(e.Ref) {
			result.Truncated = true
			continue
		}
		if len(e.Role) > MaxNodeTextBytes || len(e.Name) > MaxNodeTextBytes || (!e.Secure && len(e.Value) > MaxNodeTextBytes) {
			result.Truncated = true
		}
		e.Role = boundedText(e.Role)
		e.Name = boundedText(e.Name)
		if e.Secure {
			e.Name = ""
			e.Value = ""
		} else {
			e.Value = boundedText(e.Value)
		}
		encoded, err := json.Marshal(e)
		if err != nil || len(encoded)+1 > remaining {
			result.Truncated = true
			break
		}
		remaining -= len(encoded) + 1
		result.Elements = append(result.Elements, e)
	}
	return result
}

func boundedText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= MaxNodeTextBytes {
		return value
	}
	value = value[:MaxNodeTextBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// FrameGeometry binds a preview to the exact captured target and dimensions.
// A backend must separately verify target identity, focus and occlusion before
// injecting any resulting point; this calculation alone is not authorization.
type FrameGeometry struct {
	TargetID string
	FrameID  string
	Width    int
	Height   int
}

// PreviewPoint converts coordinates from a letterboxed preview into frame
// pixels; clicks outside the visible image and stale frame identities fail.
func PreviewPoint(frame FrameGeometry, targetID, frameID string, previewWidth, previewHeight, x, y float64) (float64, float64, error) {
	if frame.TargetID == "" || frame.FrameID == "" || frame.TargetID != targetID || frame.FrameID != frameID {
		return 0, 0, ErrStaleObservation
	}
	for _, v := range []float64{previewWidth, previewHeight, x, y} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, errors.New("invalid preview coordinates")
		}
	}
	if frame.Width <= 0 || frame.Height <= 0 || previewWidth <= 0 || previewHeight <= 0 {
		return 0, 0, errors.New("invalid preview dimensions")
	}
	scale := math.Min(previewWidth/float64(frame.Width), previewHeight/float64(frame.Height))
	w, h := float64(frame.Width)*scale, float64(frame.Height)*scale
	left, top := (previewWidth-w)/2, (previewHeight-h)/2
	if x < left || x >= left+w || y < top || y >= top+h {
		return 0, 0, fmt.Errorf("preview point is outside the captured target")
	}
	return (x - left) / scale, (y - top) / scale, nil
}
