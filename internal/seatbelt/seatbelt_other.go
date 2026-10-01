//go:build !darwin

package seatbelt

// Available is always false off macOS: process-level sandboxing needs a
// platform runner (Linux: bubblewrap/namespaces; Windows: restricted tokens)
// that this node does not ship.
func Available() bool { return false }

// New returns nil off macOS.
func New(projectDir string) Runner { return nil }
