package computeruse

import (
	"errors"
	"testing"
)

func TestAndroidPlatformBackendUnavailable(t *testing.T) {
	backend, err := NewPlatformBackend()
	if backend != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewPlatformBackend() = %v, %v; want nil, ErrUnavailable", backend, err)
	}
}
