//go:build !darwin && !linux

package seatbelt

// Available is always false on platforms without a sandbox runner (Windows:
// restricted tokens, and anything else: no implementation). macOS and Linux
// have their own files.
func Available() bool { return false }

// New returns nil on platforms without a sandbox runner.
func New(projectDir string) Runner { return nil }
