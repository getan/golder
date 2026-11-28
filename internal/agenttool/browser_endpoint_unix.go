//go:build !windows

package agenttool

import (
	"context"
	"os"
)

func browserEndpointReady(_ context.Context, path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func canonicalPipeEndpoint(string) (string, bool) { return "", false }
