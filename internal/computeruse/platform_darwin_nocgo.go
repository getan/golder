//go:build darwin && !cgo

package computeruse

import "fmt"

func NewPlatformBackend() (PlatformBackend, error) {
	return nil, fmt.Errorf("%w: this macOS build lacks CGO; install a macOS build with Computer Use support or rebuild with CGO_ENABLED=1", ErrUnavailable)
}
