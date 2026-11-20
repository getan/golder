//go:build darwin && cgo

package computeruse

import (
	"encoding/json"
	"errors"
	"testing"
)

// Exercise the dispatch gate, not just the tile hash function. The action
// observes the conversation header (tile 2) and composer (tile 14).
func TestDispatchReviewsOnlyActionRegions(t *testing.T) {
	for _, tc := range []struct {
		name string
		tile int
		wantSend bool
	}{
		{"unrelated sidebar update", 4, true},
		{"reviewed conversation header changed", 2, false},
		{"reviewed composer changed", 14, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := macFrameBinding{Target: "window", W: 400, H: 300, Tiles: make([]byte, 16*32)}
			old, err := json.Marshal(frame)
			if err != nil { t.Fatal(err) }
			frame.Tiles[tc.tile*32]++
			fresh, err := json.Marshal(frame)
			if err != nil { t.Fatal(err) }
			sent := false
			err = dispatchReviewedFrame(Target{ID: frame.Target}, string(old),
				func() (string,error) { return string(fresh),nil },
				func() (bool,error) { return false,nil }, func() error { return nil },
				func(macFrameBinding) error { sent=true; return nil }, 1<<2 | 1<<14)
			if sent != tc.wantSend || (err==nil)!=tc.wantSend { t.Fatalf("sent=%t, err=%v",sent,err) }
			if !sent && (!errors.Is(err,ErrNoInput) || StaleCode(err)!="action_region_changed") { t.Fatal(err) }
		})
	}
}
