//go:build windows

package agenttool

import (
	"context"
	"strings"
	"time"

	"github.com/getan/golder/internal/browserctl"
)

func browserEndpointReady(ctx context.Context, path string) bool {
	client, err := browserctl.Dial(path)
	if err != nil {
		return false
	}
	defer client.CloseIdleConnections()
	probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err = client.Info(probe)
	return err == nil
}

func canonicalPipeEndpoint(path string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return "", false
	}
	return strings.ToLower(path), true
}
