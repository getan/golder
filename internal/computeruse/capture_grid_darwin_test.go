//go:build darwin && cgo

package computeruse

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPreferBackingStoreKeepsOneGrid(t *testing.T) {
	for _, tc := range []struct {
		name     string
		onScreen bool
		previous string
		want     bool
	}{
		{"first capture on the current desktop", true, "", false},
		{"still on the current desktop", true, captureSourceStream, false},
		{"back on the desktop after the backing store", true, captureSourceSkyLight, true},
		{"off the current desktop", false, "", true},
		{"off the current desktop after the stream", false, captureSourceStream, true},
	} {
		if got := preferBackingStore(tc.onScreen, tc.previous); got != tc.want {
			t.Fatalf("%s: preferBackingStore(%v, %q) = %v, want %v", tc.name, tc.onScreen, tc.previous, got, tc.want)
		}
	}
}

func TestGridChangeIsRefusedAsASourceChange(t *testing.T) {
	frame := macFrameBinding{Target: "window", W: 400, H: 300, PX: 800, PY: 600, Source: captureSourceSkyLight, Tiles: make([]byte, 16*32)}
	old, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame.PX, frame.PY, frame.Source = 400, 300, captureSourceStream
	fresh, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	sent := false
	err = dispatchReviewedFrame(Target{ID: frame.Target}, string(old),
		func() (string, error) { return string(fresh), nil },
		func() (bool, error) { return false, nil }, func() error { return nil },
		func(macFrameBinding) error { sent = true; return nil }, 1<<2)
	if sent {
		t.Fatal("input was sent across a capture grid change")
	}
	if !errors.Is(err, ErrNoInput) || StaleCode(err) != "capture_source_changed" {
		t.Fatalf("err = %v (code %q)", err, StaleCode(err))
	}
}

func TestSameGridStillComparesRegions(t *testing.T) {
	frame := macFrameBinding{Target: "window", W: 400, H: 300, PX: 800, PY: 600, Source: captureSourceSkyLight, Tiles: make([]byte, 16*32)}
	old, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	frame.Tiles[2*32]++
	fresh, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	sent := false
	err = dispatchReviewedFrame(Target{ID: frame.Target}, string(old),
		func() (string, error) { return string(fresh), nil },
		func() (bool, error) { return false, nil }, func() error { return nil },
		func(macFrameBinding) error { sent = true; return nil }, 1<<2)
	if sent {
		t.Fatal("input was sent although the reviewed region changed")
	}
	if StaleCode(err) != "action_region_changed" {
		t.Fatalf("err = %v (code %q)", err, StaleCode(err))
	}
}
