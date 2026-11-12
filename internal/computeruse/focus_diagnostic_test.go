package computeruse

import (
	"errors"
	"strings"
	"testing"
)

func TestActivationRefusalIsKnownNotDispatched(t *testing.T) {
	for reason, want := range map[int]string{1: "not visible on the current", 2: "could not enumerate", 3: "another real window", 4: "off-screen", 99: "safe activation"} {
		err := macActivationError(reason)
		if !errors.Is(err, ErrNoInput) || !errors.Is(err, ErrStaleObservation) || !strings.Contains(err.Error(), want) {
			t.Fatalf("reason %d: %v", reason, err)
		}
	}
}
