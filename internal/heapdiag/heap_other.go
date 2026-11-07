//go:build !unix

package heapdiag

import (
	"fmt"
	"io"
	"os"
)

// StartFromEnv reports that signal-triggered snapshots are unavailable.
func StartFromEnv(_ io.Writer) (func(), error) {
	if os.Getenv(envDir) != "" {
		return func() {}, fmt.Errorf("%s is supported only on Unix", envDir)
	}
	return func() {}, nil
}
