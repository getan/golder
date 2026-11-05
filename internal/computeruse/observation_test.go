package computeruse

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedSnapshotRedactsSecureValues(t *testing.T) {
	items := make([]Element, MaxSnapshotNodes+1)
	items[0] = Element{Ref: "@e1", Secure: true, Value: "secret", Name: strings.Repeat("中", 300)}
	snapshot := BoundedSnapshot("snapshot-1", items)
	if !snapshot.Truncated || len(snapshot.Elements) != MaxSnapshotNodes || snapshot.Elements[0].Value != "" || snapshot.Elements[0].Name != "" {
		t.Fatalf("unexpected bounded snapshot: %+v", snapshot.Elements[0])
	}
	if len(snapshot.Elements[0].Name) > MaxNodeTextBytes || !utf8.ValidString(snapshot.Elements[0].Name) {
		t.Fatal("element name was not safely bounded")
	}
}

func TestBoundedSnapshotBoundsEncodedPayload(t *testing.T) {
	items := make([]Element, MaxSnapshotNodes)
	for i := range items {
		items[i] = Element{Ref: "ref", Role: "text", Name: strings.Repeat("\n", MaxNodeTextBytes), Value: strings.Repeat("x", MaxNodeTextBytes)}
	}
	snapshot := BoundedSnapshot("snapshot", items)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated || len(encoded) > MaxSnapshotBytes {
		t.Fatalf("truncated=%t bytes=%d", snapshot.Truncated, len(encoded))
	}
}

func TestPreviewPointRejectsStaleAndLetterbox(t *testing.T) {
	frame := FrameGeometry{TargetID: "window", FrameID: "frame", Width: 800, Height: 600}
	x, y, err := PreviewPoint(frame, "window", "frame", 1000, 600, 500, 300)
	if err != nil || x != 400 || y != 300 {
		t.Fatalf("point=(%v,%v) err=%v", x, y, err)
	}
	if _, _, err := PreviewPoint(frame, "window", "old", 1000, 600, 500, 300); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale frame: %v", err)
	}
	if _, _, err := PreviewPoint(frame, "window", "frame", 1000, 600, 50, 300); err == nil {
		t.Fatal("accepted point in letterbox")
	}
}
