//go:build !darwin || !cgo

package computeruse

// Unsupported platforms fail closed until their actual desktop backends exist.
func NewPlatformBackend() (PlatformBackend, error) {
	return nil, ErrUnavailable
}
