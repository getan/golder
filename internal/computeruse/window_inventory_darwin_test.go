//go:build darwin && cgo

package computeruse

import "testing"

func TestMacWindowVisibilityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		value int
		want  bool
	}{{-1, false}, {0, false}, {1, true}, {2, false}} {
		if got := macWindowOnScreenFlag(tc.value); got != tc.want {
			t.Errorf("on-screen flag %d = %t, want %t", tc.value, got, tc.want)
		}
	}
}
