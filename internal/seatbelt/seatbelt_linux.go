//go:build linux

package seatbelt

// Available reports whether bubblewrap can create a sandbox on this machine
// (bwrap installed, unprivileged user namespaces usable).
func Available() bool { return bwrapAvailable() }

// New returns a Runner for projectDir, or nil when bubblewrap is unusable.
func New(projectDir string) Runner {
	if !Available() {
		return nil
	}
	return &BwrapRunner{ProjectDir: projectDir}
}
