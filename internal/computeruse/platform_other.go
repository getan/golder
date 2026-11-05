//go:build (!darwin && !linux && !windows) || (darwin && !cgo) || (linux && !amd64 && !arm64)

package computeruse

// Unsupported platforms fail closed until their actual desktop backends exist.
func NewPlatformBackend() (PlatformBackend, error) {
	return nil, ErrUnavailable
}
