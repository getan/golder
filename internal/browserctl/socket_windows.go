//go:build windows

package browserctl

import (
	"fmt"
	"strings"
)

const pipePrefix = `\\.\pipe\`

// checkSocket validates a named-pipe endpoint. The pipe's DACL, created by the
// browser host, is the authorization boundary; there is no filesystem mode.
func checkSocket(path string) error {
	if !strings.HasPrefix(strings.ToLower(path), pipePrefix) {
		return fmt.Errorf("%s is not a Windows named pipe", path)
	}
	if len(path) == len(pipePrefix) {
		return fmt.Errorf("control pipe name is empty")
	}
	if strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("invalid Windows named pipe %q", path)
	}
	return nil
}
