package computeruse

import (
	"errors"
	"testing"
)

func TestStaleReasonPreservesContractAndCode(t *testing.T) {
	err := noInputError(staleReason("frame_pixels_changed", "the window pixels changed"))
	if !errors.Is(err, ErrNoInput) || !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale reason lost its contract: %v", err)
	}
	if got := StaleCode(err); got != "frame_pixels_changed" {
		t.Fatalf("StaleCode = %q, want frame_pixels_changed", got)
	}
	if got := StaleCode(errors.New("plain")); got != "" {
		t.Fatalf("StaleCode(plain) = %q, want empty", got)
	}
}
