//go:build darwin && !cgo

package computeruse

import (
	"errors"
	"strings"
	"testing"
)

func TestMacBackendWithoutCGOExplainsHowToFixIt(t *testing.T) {
	_, err := NewPlatformBackend()
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "CGO_ENABLED=1") {
		t.Fatalf("missing actionable CGO diagnostic: %v", err)
	}
}
