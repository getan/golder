//go:build darwin || linux

package appsession

import "golang.org/x/sys/unix"

// A regular file replaced by a FIFO must not stall a preview worker.
const transcriptNonblock = unix.O_NONBLOCK
