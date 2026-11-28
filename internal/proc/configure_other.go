//go:build !windows

package proc

import "os/exec"

// configure is a no-op outside Windows: a Unix GUI process does not allocate a
// console window for its children.
func configure(*exec.Cmd) {}
