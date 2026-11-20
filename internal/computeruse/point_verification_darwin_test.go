//go:build darwin && cgo

package computeruse

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
)

func macPointClassify(t *testing.T, found, metadata, overlap, hit int) string {
	t.Helper()
	var name string
	if !macPointClassifyLabel(found, metadata, overlap, hit, &name) {
		t.Fatal("classification failed")
	}
	return name
}

// The decision table runs without real windows: an overlapping overlay is only
// resolved by a hit test, and nothing is dispatched here.
func TestPointClassifySplit(t *testing.T) {
	const (
		no   = 0
		yes  = 1
		ok   = 0 // CU_POINT_OK
		fore = 2 // CU_POINT_FOREIGN_HIT
		unkn = 3 // CU_POINT_UNVERIFIABLE
	)
	for _, tc := range []struct {
		name                     string
		found, metadata, overlap int
		hit                      int
		want                     string
	}{
		{"no overlap", yes, no, no, ok, "verified"},
		{"transparent overlay resolves to target", yes, no, yes, ok, "verified"},
		{"input receiving popup blocks input", yes, no, yes, fore, "foreign_hit"},
		{"unresolvable overlay is explicit", yes, no, yes, unkn, "unverified"},
		{"target missing from visible list", no, no, no, ok, "target_not_visible"},
		{"inventory metadata unreadable", yes, yes, no, ok, "unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := macPointClassify(t, tc.found, tc.metadata, tc.overlap, tc.hit); got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInspectPointReportsVerification(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewGray(image.Rect(0, 0, 7, 9))); err != nil {
		t.Fatal(err)
	}
	b := &probeFixture{frameBoundTestBackend: frameBoundTestBackend{png: raw.Bytes()}}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := s.InspectPoint(ctx, f.ID, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hit.PointVerification == "" {
		t.Fatalf("inspection omitted point verification: %+v", hit)
	}
}
