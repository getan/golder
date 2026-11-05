//go:build linux && (amd64 || arm64)

package computeruse

// NewPlatformBackend uses the desktop's existing AT-SPI service. A desktop
// without AT-SPI fails closed rather than selecting an unrelated window.
func NewPlatformBackend() (PlatformBackend, error) {
	return newLinuxATSPIBackend()
}
