//go:build !darwin && !linux

package seatbelt

// Available is always false on platforms without a sandbox runner (Windows:
// restricted tokens, and anything else: no implementation). macOS and Linux
// have their own files.
func Available() bool { return false }

// Supported reports whether this platform has a sandbox runner AT ALL. It is
// false where isolation was never implemented, so "the runner is unavailable"
// (a hardened Linux with bubblewrap blocked) can be told apart from "there is
// no runner to have" (Windows). The first must fail closed once isolation is
// the default; the second cannot, or every command would be unusable.
func Supported() bool { return false }

// New returns nil on platforms without a sandbox runner.
func New(projectDir string) Runner { return nil }
