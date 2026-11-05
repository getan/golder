//go:build windows

package computeruse

func NewPlatformBackend() (PlatformBackend, error) {
	return newWindowsBackend()
}
