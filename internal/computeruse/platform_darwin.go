//go:build darwin && cgo

package computeruse

func NewPlatformBackend() (PlatformBackend, error) {
	return NewMacWindowCatalog(), nil
}
